package api_test

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/prompt-masters/backend/internal/ws"
)

// wsClient is a real WebSocket client with a background reader, so it never
// looks slow and "expect nothing" checks never touch the socket.
type wsClient struct {
	t      *testing.T
	conn   *websocket.Conn
	user   testUser
	events chan ws.Envelope
	count  atomic.Int64
	closed chan struct{}
}

// connect opens a socket for a game, expecting the handshake to succeed.
func (a *liveTestAPI) connect(u testUser, gameRef string) *wsClient {
	a.t.Helper()
	c, resp, err := a.tryConnect(u.Token, gameRef, nil)
	if err != nil {
		a.t.Fatalf("connecting: %v (status %v)", err, statusOf(resp))
	}
	return c
}

// tryConnect dials without asserting success, for the rejection cases.
func (a *liveTestAPI) tryConnect(token, gameRef string, header http.Header) (*wsClient, *http.Response, error) {
	a.t.Helper()
	url := "ws" + strings.TrimPrefix(a.server.URL, "http") + "/ws/games/" + gameRef
	if token != "" {
		url += "?token=" + token
	}
	conn, resp, err := websocket.DefaultDialer.Dial(url, header)
	if err != nil {
		return nil, resp, err
	}

	c := &wsClient{t: a.t, conn: conn, events: make(chan ws.Envelope, 256), closed: make(chan struct{})}
	a.t.Cleanup(func() { conn.Close() })
	go func() {
		defer close(c.closed)
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			c.count.Add(1)
			if len(c.events) == cap(c.events) {
				continue
			}
			var env ws.Envelope
			if err := json.Unmarshal(data, &env); err != nil {
				continue
			}
			select {
			case c.events <- env:
			default:
			}
		}
	}()
	return c, resp, nil
}

func statusOf(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func (c *wsClient) next(timeout time.Duration) ws.Envelope {
	c.t.Helper()
	select {
	case env := <-c.events:
		return env
	case <-time.After(timeout):
		c.t.Fatal("timed out waiting for an event")
		return ws.Envelope{}
	}
}

// nextOfType waits for one event type, skipping others.
func (c *wsClient) nextOfType(t *testing.T, want ws.Type, timeout time.Duration) ws.Envelope {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case env := <-c.events:
			if env.Type == want {
				return env
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s event", want)
			return ws.Envelope{}
		}
	}
}

func (c *wsClient) expectNothing(timeout time.Duration) {
	c.t.Helper()
	select {
	case env := <-c.events:
		c.t.Fatalf("received an unexpected %s event", env.Type)
	case <-time.After(timeout):
	}
}

func (c *wsClient) send(t *testing.T, raw string) {
	t.Helper()
	if err := c.conn.WriteMessage(websocket.TextMessage, []byte(raw)); err != nil {
		t.Fatalf("writing message: %v", err)
	}
}

func (c *wsClient) waitClosed(timeout time.Duration) bool {
	select {
	case <-c.closed:
		return true
	case <-time.After(timeout):
		return false
	}
}

func decodePayload[T any](t *testing.T, env ws.Envelope) T {
	t.Helper()
	var payload T
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decoding %s payload: %v", env.Type, err)
	}
	return payload
}

func TestWebSocketHandshakeAuthorization(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, outsider := a.user("host"), a.user("guest"), a.user("outsider")
	g := a.createGame(host, 4, guest)

	tests := []struct {
		name       string
		token      string
		gameRef    string
		wantStatus int
	}{
		{name: "no token", token: "", gameRef: g.ID, wantStatus: http.StatusUnauthorized},
		{name: "invalid token", token: "not-a-jwt", gameRef: g.ID, wantStatus: http.StatusUnauthorized},
		{name: "not a player", token: outsider.Token, gameRef: g.ID, wantStatus: http.StatusForbidden},
		{name: "unknown game", token: host.Token, gameRef: uuid.NewString(), wantStatus: http.StatusNotFound},
		{name: "malformed game", token: host.Token, gameRef: "not-a-game", wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, resp, err := a.tryConnect(tt.token, tt.gameRef, nil)
			if err == nil {
				t.Fatal("handshake succeeded, want it rejected")
			}
			if statusOf(resp) != tt.wantStatus {
				t.Errorf("status = %d, want %d", statusOf(resp), tt.wantStatus)
			}
		})
	}

	// A player connects, by game ID and by room code.
	for _, ref := range []string{g.ID, g.RoomCode} {
		c := a.connect(guest, ref)
		if env := c.next(3 * time.Second); env.Type != ws.TypeGameState {
			t.Errorf("first event = %q, want game_state", env.Type)
		}
	}
}

func TestWebSocketTokenViaSubprotocol(t *testing.T) {
	a := newLiveTestAPI(t)
	host := a.user("host")
	g := a.createGame(host, 4)

	url := "ws" + strings.TrimPrefix(a.server.URL, "http") + "/ws/games/" + g.ID
	dialer := websocket.Dialer{Subprotocols: []string{"bearer", host.Token}}
	conn, resp, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("connecting with a subprotocol token: %v (status %v)", err, statusOf(resp))
	}
	defer conn.Close()
	if got := conn.Subprotocol(); got != "bearer" {
		t.Errorf("negotiated subprotocol = %q, want bearer", got)
	}

	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("reading first event: %v", err)
	}
	var env ws.Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type != ws.TypeGameState {
		t.Errorf("first event = %s (%v), want game_state", data, err)
	}
}

func TestWebSocketRejectsForeignOrigins(t *testing.T) {
	a := newLiveTestAPI(t)
	host := a.user("host")
	g := a.createGame(host, 4)

	// The allowlist in the fixture contains https://app.example.com only.
	allowed := http.Header{"Origin": []string{"https://app.example.com"}}
	if _, _, err := a.tryConnect(host.Token, g.ID, allowed); err != nil {
		t.Errorf("allowed origin was rejected: %v", err)
	}

	foreign := http.Header{"Origin": []string{"https://evil.example.com"}}
	_, resp, err := a.tryConnect(host.Token, g.ID, foreign)
	if err == nil {
		t.Fatal("a foreign origin was accepted")
	}
	if statusOf(resp) != http.StatusForbidden {
		t.Errorf("status = %d, want 403", statusOf(resp))
	}
}

func TestWebSocketFirstEventIsTheCallersOwnState(t *testing.T) {
	a := newLiveTestAPI(t)
	alice, bob := a.user("alice"), a.user("bob")
	g := a.createGame(alice, 4, bob)
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &alice, nil)
	if _, err := a.testAPI.live.StartRound(t.Context(), uuid.MustParse(g.ID), 1, uuid.New()); err != nil {
		t.Fatal(err)
	}
	a.expect(http.StatusOK, "PUT", "/api/v1/games/"+g.ID+"/draft", &alice, map[string]any{"content": "alice's secret prompt"})

	aliceConn := a.connect(alice, g.ID)
	state := decodePayload[ws.GameStatePayload](t, aliceConn.nextOfType(t, ws.TypeGameState, 3*time.Second))
	if state.Game.GameID != g.ID || len(state.Players) != 2 {
		t.Errorf("game_state = %+v", state.Game)
	}
	if state.Round == nil || state.Round.RemainingMs <= 0 {
		t.Errorf("round = %+v, want the server's remaining time", state.Round)
	}
	if state.YourDraft == nil || state.YourDraft.Content != "alice's secret prompt" {
		t.Errorf("your_draft = %+v, want Alice's own draft", state.YourDraft)
	}

	// Bob's own connect event never contains Alice's draft.
	bobConn := a.connect(bob, g.ID)
	bobState := decodePayload[ws.GameStatePayload](t, bobConn.nextOfType(t, ws.TypeGameState, 3*time.Second))
	if bobState.YourDraft != nil {
		t.Errorf("Bob's game_state carries a draft: %+v", bobState.YourDraft)
	}
}

func TestWebSocketBroadcastsReachTheWholeRoomOnly(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, third := a.user("host"), a.user("guest"), a.user("third")
	roomA := a.createGame(host, 4, guest)
	roomB := a.createGame(third, 4)

	hostConn := a.connect(host, roomA.ID)
	guestConn := a.connect(guest, roomA.ID)
	otherConn := a.connect(third, roomB.ID)
	for _, c := range []*wsClient{hostConn, guestConn, otherConn} {
		c.nextOfType(t, ws.TypeGameState, 3*time.Second)
	}

	// A lobby change over REST reaches both clients of room A.
	newcomer := a.user("newcomer")
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+roomA.ID+"/join", &newcomer, nil)

	for _, c := range []*wsClient{hostConn, guestConn} {
		env := c.nextOfType(t, ws.TypePlayerJoined, 3*time.Second)
		joined := decodePayload[ws.PlayerJoinedPayload](t, env)
		if joined.Player.UserID != newcomer.ID.String() || joined.PlayerCount != 3 {
			t.Errorf("player_joined = %+v", joined)
		}
	}
	otherConn.expectNothing(300 * time.Millisecond)

	// Leaving is announced too, including the new host.
	a.expect(http.StatusNoContent, "POST", "/api/v1/games/"+roomA.ID+"/leave", &host, nil)
	left := decodePayload[ws.PlayerLeftPayload](t, guestConn.nextOfType(t, ws.TypePlayerLeft, 3*time.Second))
	if left.UserID != host.ID.String() || left.Username != "host" || left.NewHostID != guest.ID.String() {
		t.Errorf("player_left = %+v, want hosting passed to the guest", left)
	}
	otherConn.expectNothing(300 * time.Millisecond)
}

func TestWebSocketClientMessages(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)
	hostConn := a.connect(host, g.ID)
	guestConn := a.connect(guest, g.ID)
	for _, c := range []*wsClient{hostConn, guestConn} {
		c.nextOfType(t, ws.TypeGameState, 3*time.Second)
	}

	t.Run("player_ready is applied and broadcast", func(t *testing.T) {
		guestConn.send(t, `{"type":"player_ready","payload":{"ready":true}}`)

		for _, c := range []*wsClient{hostConn, guestConn} {
			ready := decodePayload[ws.PlayerReadyPayload](t, c.nextOfType(t, ws.TypePlayerReady, 3*time.Second))
			if ready.UserID != guest.ID.String() || !ready.Ready {
				t.Errorf("player_ready = %+v", ready)
			}
		}
		// It really changed the state, not just the broadcast.
		live := a.live(host, g.ID)
		if ready, _, _ := live.player(guest.ID); !ready {
			t.Error("readiness was not stored")
		}
	})

	t.Run("prompt_typing reaches others but not the sender", func(t *testing.T) {
		guestConn.send(t, `{"type":"prompt_typing","payload":{}}`)
		typing := decodePayload[ws.PlayerTypingPayload](t, hostConn.nextOfType(t, ws.TypePlayerTyping, 3*time.Second))
		if typing.UserID != guest.ID.String() {
			t.Errorf("player_typing = %+v", typing)
		}
		guestConn.expectNothing(300 * time.Millisecond)
	})

	t.Run("heartbeat keeps the connection without a reply", func(t *testing.T) {
		guestConn.send(t, `{"type":"heartbeat","payload":{}}`)
		guestConn.expectNothing(300 * time.Millisecond)
		if guestConn.waitClosed(0) {
			t.Error("the connection was closed after a heartbeat")
		}
	})

	t.Run("unsupported and unknown events answer with an error", func(t *testing.T) {
		for raw, wantCode := range map[string]string{
			`{"type":"prompt_test","payload":{"prompt":"hi"}}`:   ws.CodeUnsupportedEvent,
			`{"type":"submit_prompt","payload":{"prompt":"hi"}}`: ws.CodeUnsupportedEvent,
			`{"type":"nonsense","payload":{}}`:                   ws.CodeUnknownEvent,
			`not json at all`:                                    ws.CodeMalformedMessage,
		} {
			guestConn.send(t, raw)
			payload := decodePayload[ws.ErrorPayload](t, guestConn.nextOfType(t, ws.TypeError, 3*time.Second))
			if payload.Code != wantCode {
				t.Errorf("%s: error code = %q, want %q", raw, payload.Code, wantCode)
			}
			hostConn.expectNothing(100 * time.Millisecond) // errors are per-connection
		}
	})

	t.Run("a refused action reports an error without closing", func(t *testing.T) {
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		guestConn.send(t, `{"type":"player_ready","payload":{"ready":true}}`)

		payload := decodePayload[ws.ErrorPayload](t, guestConn.nextOfType(t, ws.TypeError, 3*time.Second))
		if payload.Code != ws.CodeNotAllowed {
			t.Errorf("error code = %q, want %q", payload.Code, ws.CodeNotAllowed)
		}
		if guestConn.waitClosed(0) {
			t.Error("the connection was closed by a refused action")
		}
	})
}

func TestWebSocketOneDisconnectLeavesOthersAlone(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)
	gameID := uuid.MustParse(g.ID)

	hostConn := a.connect(host, g.ID)
	guestConn := a.connect(guest, g.ID)
	for _, c := range []*wsClient{hostConn, guestConn} {
		c.nextOfType(t, ws.TypeGameState, 3*time.Second)
	}
	if n := a.hub.Connections(gameID); n != 2 {
		t.Fatalf("connections = %d, want 2", n)
	}

	guestConn.conn.Close()
	waitForCond(t, 3*time.Second, "the guest connection to be removed", func() bool {
		return a.hub.Connections(gameID) == 1
	})

	// The host keeps working.
	newcomer := a.user("newcomer")
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/join", &newcomer, nil)
	hostConn.nextOfType(t, ws.TypePlayerJoined, 3*time.Second)

	hostConn.conn.Close()
	waitForCond(t, 3*time.Second, "the room to be dropped", func() bool { return a.hub.Rooms() == 0 })
}

func TestWebSocketSlowClientDoesNotStallTheRoom(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)
	gameID := uuid.MustParse(g.ID)

	reader := a.connect(host, g.ID)
	reader.nextOfType(t, ws.TypeGameState, 3*time.Second)

	// A client that never reads, with a deliberately tiny receive buffer so it
	// falls behind quickly and predictably.
	dialer := websocket.Dialer{
		NetDial: func(network, addr string) (net.Conn, error) {
			d := &net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
				return c.Control(func(fd uintptr) {
					_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 2048)
				})
			}}
			return d.Dial(network, addr)
		},
	}
	url := "ws" + strings.TrimPrefix(a.server.URL, "http") + "/ws/games/" + g.ID + "?token=" + guest.Token
	silent, _, err := dialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("connecting the silent client: %v", err)
	}
	defer silent.Close()
	waitForCond(t, 3*time.Second, "both clients to register", func() bool { return a.hub.Connections(gameID) == 2 })

	// Broadcasting must never block on the silent client.
	msg, err := ws.Encode(ws.OutputCompletePayload{Output: strings.Repeat("y", 8<<10)})
	if err != nil {
		t.Fatal(err)
	}
	// Paced like real gameplay: a reading client keeps up easily, while the
	// client whose socket is stuffed falls behind.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 200 {
			a.hub.Broadcast(gameID, msg)
			time.Sleep(2 * time.Millisecond)
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("broadcasting blocked on the silent client")
	}

	// The silent client is dropped; the reader is not and keeps receiving.
	waitForCond(t, 10*time.Second, "the silent client to be dropped", func() bool {
		return a.hub.Connections(gameID) == 1
	})
	if reader.waitClosed(0) {
		t.Fatal("the reading client was dropped instead")
	}
	if reader.count.Load() == 0 {
		t.Fatal("the reading client received nothing during the flood")
	}
	before := reader.count.Load()
	a.hub.Broadcast(gameID, msg)
	waitForCond(t, 3*time.Second, "the reader to receive a later event", func() bool {
		return reader.count.Load() > before
	})
}

func TestWebSocketConcurrentClients(t *testing.T) {
	a := newLiveTestAPI(t)
	host := a.user("host")
	g := a.createGame(host, 6)
	gameID := uuid.MustParse(g.ID)

	players := []testUser{host}
	for i := range 5 {
		u := a.user("player" + string(rune('a'+i)))
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/join", &u, nil)
		players = append(players, u)
	}

	var wg sync.WaitGroup
	for _, u := range players {
		wg.Go(func() {
			// Two tabs each, within the per-user cap.
			first := a.connect(u, g.ID)
			second := a.connect(u, g.ID)
			for range 10 {
				first.send(t, `{"type":"prompt_typing","payload":{}}`)
				second.send(t, `{"type":"heartbeat","payload":{}}`)
			}
			first.nextOfType(t, ws.TypeGameState, 5*time.Second)
			first.conn.Close()
			second.conn.Close()
		})
	}
	wg.Wait()

	waitForCond(t, 10*time.Second, "every connection to be cleaned up", func() bool {
		return a.hub.Connections(gameID) == 0 && a.hub.Rooms() == 0
	})
}

// waitForCond polls until cond holds or the timeout passes.
func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
