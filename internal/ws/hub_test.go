package ws

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	// No goroutine started by the hub may outlive the tests.
	goleak.VerifyTestMain(m)
}

// testConfig keeps the deadlines generous: a gorilla client only answers pings
// while it is inside ReadMessage, so a test client that idles between
// assertions would otherwise look dead. The ping test sets its own tight
// values.
func testConfig() Config {
	cfg := DefaultConfig()
	cfg.PingInterval = 2 * time.Second
	cfg.PongWait = 10 * time.Second
	cfg.WriteWait = time.Second
	cfg.SendBuffer = 4
	return cfg
}

// hubFixture is a hub behind a real WebSocket server, so connections are
// genuine sockets rather than stubs.
type hubFixture struct {
	t      *testing.T
	hub    *Hub
	server *httptest.Server
	logs   *syncBuffer
	router *recordingRouter
}

type syncBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type recordingRouter struct {
	mu       sync.Mutex
	inbound  []Inbound
	onRoute  func(Inbound)
	routeErr string
}

func (r *recordingRouter) Route(_ context.Context, in Inbound) {
	r.mu.Lock()
	r.inbound = append(r.inbound, in)
	handler := r.onRoute
	r.mu.Unlock()
	if handler != nil {
		handler(in)
	}
}

func (r *recordingRouter) messages() []Inbound {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Inbound(nil), r.inbound...)
}

func newHubFixture(t *testing.T, cfg Config) *hubFixture {
	t.Helper()
	logs := &syncBuffer{}
	router := &recordingRouter{}
	hub, err := NewHub(cfg, router, log.New(logs, "", 0))
	if err != nil {
		t.Fatalf("NewHub() error = %v", err)
	}
	f := &hubFixture{t: t, hub: hub, logs: logs, router: router}

	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gameID, err := uuid.Parse(r.URL.Query().Get("game"))
		if err != nil {
			http.Error(w, "bad game", http.StatusBadRequest)
			return
		}
		userID, err := uuid.Parse(r.URL.Query().Get("user"))
		if err != nil {
			http.Error(w, "bad user", http.StatusBadRequest)
			return
		}
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		conn, err := hub.Add(r.Context(), gameID, userID, r.URL.Query().Get("name"), socket)
		if err != nil {
			socket.Close()
			return
		}
		// Marks the connection as registered, so dial can wait for a signal
		// rather than poll a count that other goroutines are changing.
		conn.SendPayload(GameStatePayload{Game: GameInfo{GameID: gameID.String()}})
	}))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hub.Shutdown(shutdownCtx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
		f.server.Close()
	})
	return f
}

// client is a test WebSocket client. A background reader drains the socket
// into events, so a client never looks slow and "expect nothing" checks never
// touch the socket: a read timeout would put gorilla's connection into a
// permanently failed state.
type client struct {
	t      *testing.T
	conn   *websocket.Conn
	gameID uuid.UUID
	userID uuid.UUID
	events chan Envelope
	count  atomic.Int64
	// closed is closed when the reader stops, i.e. the socket failed or shut.
	closed chan struct{}
}

func (f *hubFixture) dial(gameID, userID uuid.UUID, name string) *client {
	f.t.Helper()
	return f.dialWith(gameID, userID, name, true)
}

// dialSilent connects a client that never reads, to exercise back pressure.
func (f *hubFixture) dialSilent(gameID, userID uuid.UUID, name string) *client {
	f.t.Helper()
	return f.dialWith(gameID, userID, name, false)
}

func (f *hubFixture) dialWith(gameID, userID uuid.UUID, name string, read bool) *client {
	f.t.Helper()
	before := f.hub.Connections(gameID)
	url := "ws" + strings.TrimPrefix(f.server.URL, "http") +
		"/?game=" + gameID.String() + "&user=" + userID.String() + "&name=" + name
	conn, resp, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		f.t.Fatalf("dialing: %v (response %v)", err, resp)
	}
	c := &client{t: f.t, conn: conn, gameID: gameID, userID: userID, events: make(chan Envelope, 256), closed: make(chan struct{})}
	f.t.Cleanup(func() { conn.Close() })

	if read {
		go func() {
			defer close(c.closed)
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				c.count.Add(1)
				// Only decode while there is room: under a flood the point is
				// to keep draining the socket cheaply.
				if len(c.events) == cap(c.events) {
					continue
				}
				var env Envelope
				if err := json.Unmarshal(data, &env); err != nil {
					continue
				}
				select {
				case c.events <- env:
				default:
				}
			}
		}()
	}

	if read {
		// Wait for the registration marker: the connection is in its room and
		// its pumps are running. Another goroutine's broadcast can arrive
		// first, so skip anything else.
		deadline := time.After(5 * time.Second)
		for {
			select {
			case env := <-c.events:
				if env.Type == TypeGameState {
					return c
				}
			case <-deadline:
				f.t.Fatal("timed out waiting for the connection to register")
				return c
			}
		}
	}
	waitFor(f.t, 5*time.Second, "the silent connection to register", func() bool {
		return f.hub.Connections(gameID) > before
	})
	return c
}

// next returns the next event, failing if none arrives in time.
func (c *client) next(timeout time.Duration) Envelope {
	c.t.Helper()
	select {
	case env := <-c.events:
		return env
	case <-time.After(timeout):
		c.t.Fatal("timed out waiting for an event")
		return Envelope{}
	}
}

// expectNothing fails if an event arrives within timeout.
func (c *client) expectNothing(timeout time.Duration) {
	c.t.Helper()
	select {
	case env := <-c.events:
		c.t.Fatalf("received an unexpected %s event", env.Type)
	case <-time.After(timeout):
	}
}

// received is the number of messages read so far.
func (c *client) received() int64 { return c.count.Load() }

// waitClosed reports whether the server closed the socket within timeout.
func (c *client) waitClosed(timeout time.Duration) bool {
	select {
	case <-c.closed:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (c *client) send(t *testing.T, raw string) {
	t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatalf("writing message: %v", err)
	}
}

func errorEvent(t *testing.T, env Envelope) ErrorPayload {
	t.Helper()
	if env.Type != TypeError {
		t.Fatalf("event type = %q, want error", env.Type)
	}
	var payload ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decoding error payload: %v", err)
	}
	return payload
}

func mustEncode(t *testing.T, p Payload) Message {
	t.Helper()
	msg, err := Encode(p)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	return msg
}

// waitFor polls until cond holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "default is valid", mutate: func(*Config) {}},
		{name: "no ping interval", mutate: func(c *Config) { c.PingInterval = 0 }, wantErr: "WS_PING_INTERVAL"},
		{name: "pong wait below ping", mutate: func(c *Config) { c.PongWait = c.PingInterval }, wantErr: "WS_PONG_WAIT"},
		{name: "no send buffer", mutate: func(c *Config) { c.SendBuffer = 0 }, wantErr: "WS_SEND_BUFFER"},
		{name: "no message limit", mutate: func(c *Config) { c.MaxMessageBytes = 0 }, wantErr: "WS_MAX_MESSAGE_BYTES"},
		{name: "no connection limit", mutate: func(c *Config) { c.MaxConnectionsPerUser = 0 }, wantErr: "WS_MAX_CONNECTIONS_PER_USER"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want it to mention %s", err, tt.wantErr)
			}
		})
	}
	if _, err := NewHub(Config{}, nil, log.New(io.Discard, "", 0)); err == nil {
		t.Error("NewHub() with an empty config error = nil, want rejection")
	}
}

func TestHubRoomLifecycle(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameA, gameB := uuid.New(), uuid.New()

	if f.hub.Rooms() != 0 {
		t.Fatalf("Rooms() = %d before any connection, want 0", f.hub.Rooms())
	}

	first := f.dial(gameA, uuid.New(), "first")
	second := f.dial(gameA, uuid.New(), "second")
	other := f.dial(gameB, uuid.New(), "other")

	if f.hub.Rooms() != 2 || f.hub.Connections(gameA) != 2 || f.hub.Connections(gameB) != 1 {
		t.Fatalf("rooms = %d, A = %d, B = %d; want 2, 2, 1", f.hub.Rooms(), f.hub.Connections(gameA), f.hub.Connections(gameB))
	}

	// Closing one connection leaves the room and the other client alone.
	first.conn.Close()
	waitFor(t, 2*time.Second, "the first connection to be removed", func() bool { return f.hub.Connections(gameA) == 1 })
	f.hub.Broadcast(gameA, mustEncode(t, PlayerReadyPayload{UserID: second.userID.String(), Ready: true}))
	if env := second.next(time.Second); env.Type != TypePlayerReady {
		t.Errorf("surviving client received %q", env.Type)
	}

	// Emptying a room deletes it.
	second.conn.Close()
	waitFor(t, 2*time.Second, "room A to be dropped", func() bool { return f.hub.Rooms() == 1 && f.hub.Connections(gameA) == 0 })

	other.conn.Close()
	waitFor(t, 2*time.Second, "all rooms to be dropped", func() bool { return f.hub.Rooms() == 0 })
}

func TestHubBroadcastReachesOnlyItsRoom(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameA, gameB := uuid.New(), uuid.New()
	a1 := f.dial(gameA, uuid.New(), "a1")
	a2 := f.dial(gameA, uuid.New(), "a2")
	b1 := f.dial(gameB, uuid.New(), "b1")

	f.hub.Broadcast(gameA, mustEncode(t, PlayerJoinedPayload{Player: PlayerInfo{Username: "newcomer"}, PlayerCount: 3}))

	for _, c := range []*client{a1, a2} {
		env := c.next(time.Second)
		if env.Type != TypePlayerJoined {
			t.Errorf("client in room A received %q, want player_joined", env.Type)
		}
	}
	b1.expectNothing(200 * time.Millisecond)

	// Broadcasting to a game with no room is a no-op, not a panic.
	f.hub.Broadcast(uuid.New(), mustEncode(t, PlayerReadyPayload{}))
}

func TestHubTargetedSends(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameID := uuid.New()
	alice, bob := uuid.New(), uuid.New()
	aliceTab1 := f.dial(gameID, alice, "alice")
	aliceTab2 := f.dial(gameID, alice, "alice")
	bobClient := f.dial(gameID, bob, "bob")

	// Both of Alice's tabs get her targeted event; Bob gets none.
	f.hub.SendToUser(gameID, alice, mustEncode(t, OutputChunkPayload{RoundNumber: 1, TestNumber: 2, Chunk: "alice only"}))
	for _, c := range []*client{aliceTab1, aliceTab2} {
		env := c.next(time.Second)
		if env.Type != TypeOutputChunk {
			t.Errorf("Alice's tab received %q", env.Type)
		}
		if !strings.Contains(string(env.Payload), "alice only") {
			t.Errorf("payload = %s", env.Payload)
		}
	}
	bobClient.expectNothing(200 * time.Millisecond)

	// BroadcastExcept skips its subject.
	f.hub.BroadcastExcept(gameID, alice, mustEncode(t, PlayerTypingPayload{UserID: alice.String()}))
	if env := bobClient.next(time.Second); env.Type != TypePlayerTyping {
		t.Errorf("Bob received %q, want player_typing", env.Type)
	}
	aliceTab1.expectNothing(200 * time.Millisecond)

	// Sending to a user who is not in the room is a no-op.
	f.hub.SendToUser(gameID, uuid.New(), mustEncode(t, PlayerReadyPayload{}))
}

func TestHubCapsConnectionsPerUser(t *testing.T) {
	cfg := testConfig()
	cfg.MaxConnectionsPerUser = 2
	f := newHubFixture(t, cfg)
	gameID, userID := uuid.New(), uuid.New()

	first := f.dial(gameID, userID, "tab1")
	second := f.dial(gameID, userID, "tab2")
	if f.hub.Connections(gameID) != 2 {
		t.Fatalf("connections = %d, want 2", f.hub.Connections(gameID))
	}

	third := f.dial(gameID, userID, "tab3")
	waitFor(t, 2*time.Second, "the oldest connection to be evicted", func() bool { return f.hub.Connections(gameID) == 2 })

	// The oldest socket is closed; the newer ones still receive events.
	if !first.waitClosed(2 * time.Second) {
		t.Error("the evicted connection is still open")
	}
	f.hub.Broadcast(gameID, mustEncode(t, PlayerReadyPayload{UserID: userID.String(), Ready: true}))
	for _, c := range []*client{second, third} {
		if env := c.next(time.Second); env.Type != TypePlayerReady {
			t.Errorf("surviving connection received %q", env.Type)
		}
	}
	if !strings.Contains(f.logs.String(), "op=evict_connection") {
		t.Errorf("eviction was not logged: %s", f.logs.String())
	}
}

func TestHubDisconnectsSlowClientWithoutBlockingTheRoom(t *testing.T) {
	cfg := testConfig()
	cfg.SendBuffer = 16
	cfg.WriteWait = 200 * time.Millisecond
	f := newHubFixture(t, cfg)
	gameID := uuid.New()
	slowID, fastID := uuid.New(), uuid.New()

	f.dialSilent(gameID, slowID, "slow")
	fast := f.dial(gameID, fastID, "fast")

	// Flood the room. The slow client's buffers fill and it is dropped, while
	// the reading client keeps up.
	big := strings.Repeat("x", 16<<10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 150 {
			f.hub.Broadcast(gameID, mustEncode(t, OutputCompletePayload{Output: big}))
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("broadcasting blocked on the slow client")
	}

	waitFor(t, 10*time.Second, "the slow client to be dropped", func() bool { return f.hub.Connections(gameID) == 1 })
	if fast.waitClosed(0) {
		t.Fatal("the reading client was dropped too")
	}
	if !strings.Contains(f.logs.String(), "op=drop_slow_client") {
		t.Errorf("slow client was not logged: %s", f.logs.String())
	}
	if fast.received() == 0 {
		t.Error("the reading client received nothing while the slow one was being dropped")
	}

	// The reading client is still connected and still receives later events.
	before := fast.received()
	f.hub.Broadcast(gameID, mustEncode(t, PlayerReadyPayload{UserID: fastID.String(), Ready: true}))
	waitFor(t, 3*time.Second, "the reading client to receive a later event", func() bool {
		return fast.received() > before
	})
}

func TestHubRoutesClientMessages(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameID, userID := uuid.New(), uuid.New()
	c := f.dial(gameID, userID, "player")

	c.send(t, `{"type":"player_ready","payload":{"ready":true}}`)
	waitFor(t, 2*time.Second, "the message to be routed", func() bool { return len(f.router.messages()) == 1 })

	in := f.router.messages()[0]
	if in.Type != TypeClientPlayerReady || in.GameID != gameID || in.UserID != userID || in.Username != "player" {
		t.Errorf("inbound = %+v, want the message with identity from the connection", in)
	}
	var ready ClientPlayerReady
	if err := json.Unmarshal(in.Payload, &ready); err != nil || ready.Ready == nil || !*ready.Ready {
		t.Errorf("payload = %s, %v", in.Payload, err)
	}

	// A router reply goes back to that one connection.
	in.Reply(PlayerReadyPayload{UserID: userID.String(), Ready: true})
	if env := c.next(time.Second); env.Type != TypePlayerReady {
		t.Errorf("reply event = %q", env.Type)
	}

	// heartbeat is absorbed by the hub and never routed.
	c.send(t, `{"type":"heartbeat","payload":{}}`)
	time.Sleep(100 * time.Millisecond)
	if n := len(f.router.messages()); n != 1 {
		t.Errorf("routed %d messages, want heartbeat handled by the hub", n)
	}
}

func TestHubAnswersBadMessagesWithAnErrorEvent(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameID, userID := uuid.New(), uuid.New()
	c := f.dial(gameID, userID, "player")

	for _, tt := range []struct {
		name, raw, wantCode string
	}{
		{name: "not JSON", raw: "definitely not json", wantCode: CodeMalformedMessage},
		{name: "no type", raw: `{"payload":{}}`, wantCode: CodeMalformedMessage},
		{name: "empty object", raw: `{}`, wantCode: CodeMalformedMessage},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c.send(t, tt.raw)
			payload := errorEvent(t, c.next(time.Second))
			if payload.Code != tt.wantCode {
				t.Errorf("error code = %q, want %q", payload.Code, tt.wantCode)
			}
		})
	}

	// The connection survives every one of them.
	if f.hub.Connections(gameID) != 1 {
		t.Fatalf("connections = %d, want the client still connected", f.hub.Connections(gameID))
	}
	f.hub.Broadcast(gameID, mustEncode(t, PlayerReadyPayload{Ready: true}))
	if env := c.next(time.Second); env.Type != TypePlayerReady {
		t.Errorf("event after bad messages = %q", env.Type)
	}
}

func TestHubWithoutRouterReportsUnsupportedEvents(t *testing.T) {
	logs := &syncBuffer{}
	hub, err := NewHub(testConfig(), nil, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if _, err := hub.Add(r.Context(), uuid.New(), uuid.New(), "player", socket); err != nil {
			socket.Close()
		}
	}))
	defer server.Close()
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hub.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() error = %v", err)
		}
	}()

	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dialing: %v", err)
	}
	defer conn.Close()

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"prompt_test","payload":{"prompt":"hi"}}`)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	if payload := errorEvent(t, env); payload.Code != CodeUnsupportedEvent {
		t.Errorf("error code = %q, want %q", payload.Code, CodeUnsupportedEvent)
	}
}

func TestHubPingKeepsConnectionsAndDropsDeadOnes(t *testing.T) {
	cfg := testConfig()
	cfg.PingInterval = 30 * time.Millisecond
	cfg.PongWait = 120 * time.Millisecond
	f := newHubFixture(t, cfg)
	gameID := uuid.New()

	// A client that answers pings (the default handler does) stays connected.
	f.dial(gameID, uuid.New(), "alive")
	time.Sleep(400 * time.Millisecond)
	if f.hub.Connections(gameID) != 1 {
		t.Fatalf("connections = %d, want the responsive client kept", f.hub.Connections(gameID))
	}

	// A client that stops answering is dropped once PongWait passes.
	dead := f.dialSilent(gameID, uuid.New(), "dead")
	if err := dead.conn.UnderlyingConn().Close(); err != nil {
		t.Fatalf("closing the underlying socket: %v", err)
	}
	waitFor(t, 3*time.Second, "the dead connection to be dropped", func() bool { return f.hub.Connections(gameID) == 1 })
}

func TestHubUnregisterIsIdempotent(t *testing.T) {
	f := newHubFixture(t, testConfig())
	gameID, userID := uuid.New(), uuid.New()
	f.dial(gameID, userID, "player")

	var conn *Connection
	for _, c := range f.hub.room(gameID).connections() {
		conn = c
	}
	if conn == nil {
		t.Fatal("connection not registered")
	}

	// Every path that tears a connection down, several times over.
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			conn.Close("bye")
			conn.stop(websocket.CloseNormalClosure, "again")
			conn.finish()
			conn.Send(mustEncode(t, PlayerReadyPayload{}))
		})
	}
	wg.Wait()

	waitFor(t, 2*time.Second, "the room to be dropped", func() bool { return f.hub.Rooms() == 0 })
	if f.hub.Connections(gameID) != 0 {
		t.Errorf("connections = %d, want 0", f.hub.Connections(gameID))
	}
}

func TestHubShutdownClosesEverything(t *testing.T) {
	logs := &syncBuffer{}
	hub, err := NewHub(testConfig(), nil, log.New(logs, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	gameID := uuid.New()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		socket, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		if _, err := hub.Add(r.Context(), gameID, uuid.New(), "player", socket); err != nil {
			socket.Close()
		}
	}))
	defer server.Close()

	var clients []*websocket.Conn
	for range 5 {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
		if err != nil {
			t.Fatalf("dialing: %v", err)
		}
		defer conn.Close()
		clients = append(clients, conn)
	}
	waitFor(t, 2*time.Second, "all clients to register", func() bool { return hub.Connections(gameID) == 5 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hub.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if hub.Rooms() != 0 {
		t.Errorf("Rooms() = %d after shutdown, want 0", hub.Rooms())
	}

	// Every client sees the socket close.
	for i, conn := range clients {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, err := conn.ReadMessage(); err == nil {
			t.Errorf("client %d is still connected after shutdown", i)
		}
	}

	// Adding after shutdown is refused rather than leaking a goroutine.
	if _, err := hub.Add(context.Background(), gameID, uuid.New(), "late", nil); err == nil {
		t.Error("Add() after Shutdown() error = nil, want ErrHubClosed")
	}
}

func TestHubConcurrentTraffic(t *testing.T) {
	cfg := testConfig()
	cfg.SendBuffer = 256 // big enough that readers keep up and stay connected
	f := newHubFixture(t, cfg)
	games := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}

	var wg sync.WaitGroup
	for _, gameID := range games {
		for range 6 {
			wg.Go(func() {
				c := f.dial(gameID, uuid.New(), "player")
				for range 20 {
					f.hub.Broadcast(gameID, mustEncode(t, TimerTickPayload{RemainingMs: 1000}))
					f.hub.SendToUser(gameID, c.userID, mustEncode(t, PlayerReadyPayload{Ready: true}))
					// The socket may already be closing; a failed write here is
					// not what this test is about.
					_ = c.conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"heartbeat","payload":{}}`))
				}
				c.conn.Close()
			})
		}
	}
	wg.Wait()

	waitFor(t, 10*time.Second, "all rooms to empty", func() bool { return f.hub.Rooms() == 0 })
}
