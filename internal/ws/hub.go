package ws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

type Config struct {
	// PingInterval is how often the server pings an idle connection.
	PingInterval time.Duration
	// PongWait is how long a connection may stay silent before it is closed.
	// It must exceed PingInterval.
	PongWait time.Duration
	// WriteWait bounds a single socket write.
	WriteWait time.Duration
	// SendBuffer is the per-connection outbound queue length. A client that
	// lets it fill up is disconnected.
	SendBuffer int
	// MaxMessageBytes is the largest client message accepted.
	MaxMessageBytes int64
	// MaxConnectionsPerUser caps sockets one user may hold in one room; the
	// oldest is closed beyond it.
	MaxConnectionsPerUser int
}

func DefaultConfig() Config {
	return Config{
		PingInterval:          30 * time.Second,
		PongWait:              60 * time.Second,
		WriteWait:             10 * time.Second,
		SendBuffer:            64,
		MaxMessageBytes:       16 << 10,
		MaxConnectionsPerUser: 3,
	}
}

func (c Config) Validate() error {
	var errs []error
	if c.PingInterval <= 0 {
		errs = append(errs, errors.New("WS_PING_INTERVAL must be positive"))
	}
	if c.PongWait <= c.PingInterval {
		errs = append(errs, errors.New("WS_PONG_WAIT must be greater than WS_PING_INTERVAL"))
	}
	if c.WriteWait <= 0 {
		errs = append(errs, errors.New("WS_WRITE_WAIT must be positive"))
	}
	if c.SendBuffer <= 0 {
		errs = append(errs, errors.New("WS_SEND_BUFFER must be positive"))
	}
	if c.MaxMessageBytes <= 0 {
		errs = append(errs, errors.New("WS_MAX_MESSAGE_BYTES must be positive"))
	}
	if c.MaxConnectionsPerUser <= 0 {
		errs = append(errs, errors.New("WS_MAX_CONNECTIONS_PER_USER must be positive"))
	}
	return errors.Join(errs...)
}

// ErrHubClosed is returned when a connection is added after shutdown.
var ErrHubClosed = errors.New("websocket hub is shut down")

// Hub owns one room per game and delivers events to the connections in them.
// It is safe for concurrent use and carries no game rules: services publish
// events, the hub delivers them.
//
// Locking: the hub lock guards the room map and a room lock guards its
// connections. Both are released before any socket I/O, so a slow or blocked
// client can never stall the hub or its room.
type Hub struct {
	cfg    Config
	router Router
	logger *log.Logger

	mu     sync.RWMutex
	rooms  map[uuid.UUID]*room
	closed bool
	// pumps tracks the read and write goroutines of every connection.
	pumps sync.WaitGroup
}

func NewHub(cfg Config, router Router, logger *log.Logger) (*Hub, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Hub{cfg: cfg, router: router, logger: logger, rooms: map[uuid.UUID]*room{}}, nil
}

// Add registers an upgraded socket into the game's room and starts its pumps.
// The room is created on the first connection.
func (h *Hub) Add(ctx context.Context, gameID, userID uuid.UUID, username string, socket *websocket.Conn) (*Connection, error) {
	if gameID == uuid.Nil || userID == uuid.Nil {
		return nil, errors.New("ws: game and user IDs are required")
	}

	conn := &Connection{
		hub:      h,
		socket:   socket,
		gameID:   gameID,
		userID:   userID,
		username: username,
		send:     make(chan Message, h.cfg.SendBuffer),
		done:     make(chan struct{}),
	}

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrHubClosed
	}
	r, ok := h.rooms[gameID]
	if !ok {
		r = newRoom(gameID)
		h.rooms[gameID] = r
	}
	// Count the pumps while still holding the lock: Shutdown marks the hub
	// closed under the same lock before waiting, so the counter can never be
	// raised once the wait has begun.
	h.pumps.Add(2)
	h.mu.Unlock()

	conn.room = r
	evicted := r.add(conn, h.cfg.MaxConnectionsPerUser)
	for _, old := range evicted {
		h.logger.Printf("ws op=evict_connection game_id=%s user_id=%s reason=connection_limit", gameID, userID)
		old.stop(websocket.ClosePolicyViolation, "too many connections")
	}

	go func() {
		defer h.pumps.Done()
		conn.writePump()
	}()
	go func() {
		defer h.pumps.Done()
		conn.readPump(ctx)
	}()
	return conn, nil
}

// Broadcast delivers msg to every connection in the game's room.
func (h *Hub) Broadcast(gameID uuid.UUID, msg Message) {
	r := h.room(gameID)
	if r == nil {
		return
	}
	// Snapshot under the room's read lock, then send outside it.
	for _, c := range r.connections() {
		c.Send(msg)
	}
}

// SendToUser delivers msg to one user's connections in the game's room. It is
// how per-player events (errors, a player's own draft or output) are sent.
func (h *Hub) SendToUser(gameID, userID uuid.UUID, msg Message) {
	r := h.room(gameID)
	if r == nil {
		return
	}
	for _, c := range r.userConnections(userID) {
		c.Send(msg)
	}
}

// BroadcastExcept delivers msg to everyone in the room but one user, for
// presence events that should not echo back to their source.
func (h *Hub) BroadcastExcept(gameID, userID uuid.UUID, msg Message) {
	r := h.room(gameID)
	if r == nil {
		return
	}
	for _, c := range r.connections() {
		if c.userID != userID {
			c.Send(msg)
		}
	}
}

// Shutdown closes every connection and waits for their goroutines to finish,
// or until ctx is done.
func (h *Hub) Shutdown(ctx context.Context) error {
	h.mu.Lock()
	h.closed = true
	rooms := make([]*room, 0, len(h.rooms))
	for _, r := range h.rooms {
		rooms = append(rooms, r)
	}
	h.mu.Unlock()

	for _, r := range rooms {
		for _, c := range r.connections() {
			c.stop(websocket.CloseGoingAway, "server shutting down")
		}
	}

	finished := make(chan struct{})
	go func() {
		h.pumps.Wait()
		close(finished)
	}()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Rooms is the number of rooms with at least one connection.
func (h *Hub) Rooms() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.rooms)
}

// Connections is the number of sockets in a game's room.
func (h *Hub) Connections(gameID uuid.UUID) int {
	r := h.room(gameID)
	if r == nil {
		return 0
	}
	return r.size()
}

func (h *Hub) room(gameID uuid.UUID) *room {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.rooms[gameID]
}

// remove unregisters a connection and drops the room once it is empty.
func (h *Hub) remove(c *Connection) {
	if c.room == nil {
		return
	}
	empty := c.room.remove(c)
	if !empty {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Re-check under the hub lock: someone may have joined meanwhile.
	if r, ok := h.rooms[c.gameID]; ok && r == c.room && r.size() == 0 {
		delete(h.rooms, c.gameID)
	}
}

// room holds the connections of one game.
type room struct {
	gameID uuid.UUID

	mu     sync.RWMutex
	conns  map[*Connection]struct{}
	byUser map[uuid.UUID][]*Connection
}

func newRoom(gameID uuid.UUID) *room {
	return &room{gameID: gameID, conns: map[*Connection]struct{}{}, byUser: map[uuid.UUID][]*Connection{}}
}

// add registers c and returns connections of the same user to close because
// they exceed maxPerUser, oldest first.
func (r *room) add(c *Connection, maxPerUser int) []*Connection {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.conns[c] = struct{}{}
	r.byUser[c.userID] = append(r.byUser[c.userID], c)

	existing := r.byUser[c.userID]
	if len(existing) <= maxPerUser {
		return nil
	}
	evicted := make([]*Connection, len(existing)-maxPerUser)
	copy(evicted, existing[:len(existing)-maxPerUser])
	return evicted
}

// remove unregisters c and reports whether the room is now empty.
func (r *room) remove(c *Connection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.conns, c)
	remaining := r.byUser[c.userID][:0]
	for _, existing := range r.byUser[c.userID] {
		if existing != c {
			remaining = append(remaining, existing)
		}
	}
	if len(remaining) == 0 {
		delete(r.byUser, c.userID)
	} else {
		r.byUser[c.userID] = remaining
	}
	return len(r.conns) == 0
}

func (r *room) connections() []*Connection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Connection, 0, len(r.conns))
	for c := range r.conns {
		out = append(out, c)
	}
	return out
}

func (r *room) userConnections(userID uuid.UUID) []*Connection {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]*Connection(nil), r.byUser[userID]...)
}

func (r *room) size() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.conns)
}

// Connection is one player's socket. The write pump is the only goroutine
// that writes to it.
type Connection struct {
	hub      *Hub
	room     *room
	socket   *websocket.Conn
	gameID   uuid.UUID
	userID   uuid.UUID
	username string

	send chan Message
	// done is closed once, instead of closing send, so a concurrent Send can
	// never write to a closed channel.
	done       chan struct{}
	stopOnce   sync.Once
	removeOnce sync.Once

	closeMu   sync.Mutex
	closeCode int
	closeText string
}

func (c *Connection) UserID() uuid.UUID { return c.userID }
func (c *Connection) GameID() uuid.UUID { return c.gameID }

// Send queues msg without blocking. A client whose buffer is full is
// disconnected rather than allowed to hold up the room; it gets fresh state
// when it reconnects.
func (c *Connection) Send(msg Message) bool {
	select {
	case <-c.done:
		return false
	default:
	}

	select {
	case c.send <- msg:
		return true
	case <-c.done:
		return false
	default:
		c.hub.logger.Printf("ws op=drop_slow_client game_id=%s user_id=%s event=%s buffer=%d",
			c.gameID, c.userID, msg.Type, cap(c.send))
		c.stop(websocket.CloseTryAgainLater, "client too slow")
		return false
	}
}

// Close disconnects the connection gracefully.
func (c *Connection) Close(reason string) {
	c.stop(websocket.CloseNormalClosure, reason)
}

// stop signals both pumps to finish. It is safe to call repeatedly and from
// any goroutine.
func (c *Connection) stop(code int, text string) {
	c.stopOnce.Do(func() {
		c.closeMu.Lock()
		c.closeCode, c.closeText = code, text
		c.closeMu.Unlock()
		close(c.done)
	})
}

func (c *Connection) closeFrame() (int, string) {
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closeCode == 0 {
		return websocket.CloseNormalClosure, ""
	}
	return c.closeCode, c.closeText
}

// writePump owns the socket's write side and the ping ticker. On exit it
// sends a close frame, closes the socket, which also unblocks the read pump,
// and unregisters the connection.
func (c *Connection) writePump() {
	ticker := time.NewTicker(c.hub.cfg.PingInterval)
	defer func() {
		ticker.Stop()
		c.finish()
	}()

	for {
		select {
		case msg := <-c.send:
			if err := c.write(websocket.TextMessage, msg.Bytes()); err != nil {
				return
			}
		case <-ticker.C:
			if err := c.write(websocket.PingMessage, nil); err != nil {
				return
			}
		case <-c.done:
			code, text := c.closeFrame()
			_ = c.write(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
			return
		}
	}
}

func (c *Connection) write(messageType int, data []byte) error {
	if err := c.socket.SetWriteDeadline(time.Now().Add(c.hub.cfg.WriteWait)); err != nil {
		return err
	}
	return c.socket.WriteMessage(messageType, data)
}

// readPump reads client messages until the socket fails or the deadline
// passes with no pong.
func (c *Connection) readPump(ctx context.Context) {
	defer c.stop(websocket.CloseNormalClosure, "")

	c.socket.SetReadLimit(c.hub.cfg.MaxMessageBytes)
	_ = c.socket.SetReadDeadline(time.Now().Add(c.hub.cfg.PongWait))
	c.socket.SetPongHandler(func(string) error {
		return c.socket.SetReadDeadline(time.Now().Add(c.hub.cfg.PongWait))
	})

	for {
		_, data, err := c.socket.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				c.hub.logger.Printf("ws op=read game_id=%s user_id=%s error=%q", c.gameID, c.userID, err)
			}
			return
		}
		// Any traffic proves the client is alive.
		_ = c.socket.SetReadDeadline(time.Now().Add(c.hub.cfg.PongWait))
		c.handle(ctx, data)
	}
}

// handle decodes one client message and routes it. A malformed or unknown
// message answers with an error event instead of dropping the connection.
func (c *Connection) handle(ctx context.Context, data []byte) {
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil || env.Type == "" {
		c.sendError(CodeMalformedMessage, "Message must be a JSON object with a type.")
		return
	}
	if env.Type == TypeClientHeartbeat {
		// Reading it already extended the deadline.
		return
	}
	if c.hub.router == nil {
		c.sendError(CodeUnsupportedEvent, fmt.Sprintf("Event %q is not supported yet.", env.Type))
		return
	}

	c.hub.router.Route(ctx, Inbound{
		GameID:   c.gameID,
		UserID:   c.userID,
		Username: c.username,
		Type:     env.Type,
		Payload:  env.Payload,
		Reply:    c.SendPayload,
	})
}

// SendPayload encodes and queues one event for this connection.
func (c *Connection) SendPayload(p Payload) {
	msg, err := Encode(p)
	if err != nil {
		c.hub.logger.Printf("ws op=encode game_id=%s user_id=%s error=%q", c.gameID, c.userID, err)
		return
	}
	c.Send(msg)
}

func (c *Connection) sendError(code, message string) {
	c.SendPayload(ErrorPayload{Code: code, Message: message})
}

// finish closes the socket and unregisters the connection exactly once.
func (c *Connection) finish() {
	c.removeOnce.Do(func() {
		c.stop(websocket.CloseNormalClosure, "")
		_ = c.socket.Close()
		c.hub.remove(c)
	})
}
