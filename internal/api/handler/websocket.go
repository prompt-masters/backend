package handler

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/prompt-masters/backend/internal/api/response"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/service"
	"github.com/prompt-masters/backend/internal/util"
	"github.com/prompt-masters/backend/internal/ws"
)

// tokenSubprotocol is the Sec-WebSocket-Protocol prefix accepted as an
// alternative to the token query parameter: "bearer, <token>".
const tokenSubprotocol = "bearer"

// WebSocketHandler upgrades game connections.
//
// Authentication happens before the upgrade, so failures are ordinary HTTP
// responses: 401 without a valid token, 404 for an unknown game and 403 for a
// user who is not one of its players. Browsers cannot set headers on a
// WebSocket handshake, so the access token is read from the "token" query
// parameter, or from a "bearer, <token>" subprotocol.
type WebSocketHandler struct {
	hub            *ws.Hub
	live           *service.LiveStateService
	jwtSecret      string
	allowedOrigins []string
	logger         *log.Logger
	upgrader       websocket.Upgrader
}

func NewWebSocketHandler(hub *ws.Hub, live *service.LiveStateService, jwtSecret string, allowedOrigins []string, logger *log.Logger) *WebSocketHandler {
	h := &WebSocketHandler{
		hub:            hub,
		live:           live,
		jwtSecret:      jwtSecret,
		allowedOrigins: allowedOrigins,
		logger:         logger,
	}
	h.upgrader = websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		ReadBufferSize:   4096,
		WriteBufferSize:  4096,
		CheckOrigin:      h.checkOrigin,
	}
	return h
}

// checkOrigin accepts requests with no Origin (native and test clients),
// same-origin requests, and origins on the configured allowlist.
func (h *WebSocketHandler) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return true
	}
	if slices.ContainsFunc(h.allowedOrigins, func(allowed string) bool {
		return strings.EqualFold(allowed, origin) || strings.EqualFold(allowed, parsed.Host)
	}) {
		return true
	}
	h.logger.Printf("ws op=reject_origin origin=%q host=%q", origin, r.Host)
	return false
}

func (h *WebSocketHandler) Connect(w http.ResponseWriter, r *http.Request) {
	token, fromSubprotocol := tokenFromRequest(r)
	if token == "" {
		response.WriteError(w, http.StatusUnauthorized, "A valid access token is required.")
		return
	}
	userID, err := util.ParseJWT(h.jwtSecret, token)
	if err != nil {
		response.WriteError(w, http.StatusUnauthorized, "A valid access token is required.")
		return
	}

	// Authorize before upgrading: this both proves membership and provides
	// the state the client needs first.
	live, err := h.live.GetLiveState(r.Context(), userID, r.PathValue("id"))
	if err != nil {
		h.writeHandshakeError(w, err)
		return
	}

	var responseHeader http.Header
	if fromSubprotocol {
		responseHeader = http.Header{"Sec-Websocket-Protocol": []string{tokenSubprotocol}}
	}
	socket, err := h.upgrader.Upgrade(w, r, responseHeader)
	if err != nil {
		// Upgrade has already written the error response.
		return
	}

	// The request context is cancelled once this handler returns, but the
	// connection outlives it.
	ctx := context.WithoutCancel(r.Context())
	conn, err := h.hub.Add(ctx, live.Lobby.GameID, userID, usernameOf(live, userID), socket)
	if err != nil {
		h.logger.Printf("ws op=register game_id=%s user_id=%s error=%q", live.Lobby.GameID, userID, err)
		_ = socket.Close()
		return
	}

	// The connecting player gets the full state, including their own draft.
	conn.SendPayload(ws.GameStateFrom(live, true))
}

// writeHandshakeError answers a failed handshake before any upgrade.
func (h *WebSocketHandler) writeHandshakeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrNotGamePlayer):
		response.WriteError(w, http.StatusForbidden, "Only players of this game can connect to it.")
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrStateNotFound):
		response.WriteError(w, http.StatusNotFound, "The requested game was not found.")
	case errors.Is(err, domain.ErrStateUnavailable):
		response.WriteError(w, http.StatusServiceUnavailable, "Live game state is temporarily unavailable. Please retry shortly.")
	default:
		h.logger.Printf("ws op=handshake error=%q", err)
		response.WriteError(w, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}

// tokenFromRequest reads the access token from the query parameter or the
// "bearer, <token>" subprotocol, reporting which was used.
func tokenFromRequest(r *http.Request) (string, bool) {
	if token := r.URL.Query().Get("token"); token != "" {
		return token, false
	}
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		parts := strings.Split(header, ",")
		if len(parts) < 2 || !strings.EqualFold(strings.TrimSpace(parts[0]), tokenSubprotocol) {
			continue
		}
		if token := strings.TrimSpace(parts[1]); token != "" {
			return token, true
		}
	}
	return "", false
}

func usernameOf(live *domain.LiveGameState, userID uuid.UUID) string {
	for _, p := range live.Players {
		if p.UserID == userID {
			return p.Username
		}
	}
	return ""
}
