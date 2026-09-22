package service

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/ws"
)

// recordingHub captures what the router publishes.
type recordingHub struct {
	mu        sync.Mutex
	broadcast []ws.Message
	targeted  []ws.Message
	excluded  []uuid.UUID
}

func (h *recordingHub) Broadcast(_ uuid.UUID, msg ws.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast = append(h.broadcast, msg)
}

func (h *recordingHub) SendToUser(_ uuid.UUID, _ uuid.UUID, msg ws.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.targeted = append(h.targeted, msg)
}

func (h *recordingHub) BroadcastExcept(_ uuid.UUID, userID uuid.UUID, msg ws.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcast = append(h.broadcast, msg)
	h.excluded = append(h.excluded, userID)
}

func (h *recordingHub) types() []ws.Type {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := []ws.Type{}
	for _, m := range h.broadcast {
		out = append(out, m.Type)
	}
	return out
}

// routerFixture wires a router onto the live-state fixture.
type routerFixture struct {
	*liveFixture
	hub     *recordingHub
	router  *WSRouter
	replies []ws.Payload
}

func newRouterFixture(t *testing.T, status domain.GameStatus) *routerFixture {
	t.Helper()
	live := newLiveFixture(t, status, true)
	hub := &recordingHub{}
	return &routerFixture{liveFixture: live, hub: hub, router: NewWSRouter(live.svc, hub, log.New(live.logs, "", 0))}
}

// route sends one client message as userID and collects any reply.
func (f *routerFixture) route(userID uuid.UUID, eventType ws.Type, payload string) []ws.Payload {
	var replies []ws.Payload
	f.router.Route(context.Background(), ws.Inbound{
		GameID:  f.game.ID,
		UserID:  userID,
		Type:    eventType,
		Payload: json.RawMessage(payload),
		Reply:   func(p ws.Payload) { replies = append(replies, p) },
	})
	return replies
}

func errorReply(t *testing.T, replies []ws.Payload) ws.ErrorPayload {
	t.Helper()
	if len(replies) != 1 {
		t.Fatalf("got %d replies, want exactly one error", len(replies))
	}
	payload, ok := replies[0].(ws.ErrorPayload)
	if !ok {
		t.Fatalf("reply = %T, want an error payload", replies[0])
	}
	return payload
}

func TestWSRouterPlayerReady(t *testing.T) {
	t.Run("marks ready and broadcasts", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusWaiting)

		if replies := f.route(f.guest, ws.TypeClientPlayerReady, `{"ready":true}`); len(replies) != 0 {
			t.Fatalf("replies = %+v, want none", replies)
		}
		ready, _ := f.state.ListReady(context.Background(), f.game.ID)
		if !slices.Equal(ready, []uuid.UUID{f.guest}) {
			t.Errorf("ready = %v, want the guest", ready)
		}
		if got := f.hub.types(); !slices.Equal(got, []ws.Type{ws.TypePlayerReady}) {
			t.Errorf("broadcast = %v, want one player_ready", got)
		}
	})

	t.Run("an empty payload means ready", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusWaiting)
		f.route(f.guest, ws.TypeClientPlayerReady, ``)
		if ready, _ := f.state.ListReady(context.Background(), f.game.ID); len(ready) != 1 {
			t.Errorf("ready = %v, want the guest", ready)
		}
	})

	t.Run("ready false unmarks", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusWaiting)
		f.route(f.guest, ws.TypeClientPlayerReady, `{"ready":true}`)
		f.route(f.guest, ws.TypeClientPlayerReady, `{"ready":false}`)
		if ready, _ := f.state.ListReady(context.Background(), f.game.ID); len(ready) != 0 {
			t.Errorf("ready = %v, want empty", ready)
		}
	})

	t.Run("refusals answer the caller only", func(t *testing.T) {
		tests := []struct {
			name     string
			status   domain.GameStatus
			caller   func(f *routerFixture) uuid.UUID
			payload  string
			wantCode string
		}{
			{name: "outsider", status: domain.GameStatusWaiting, caller: func(*routerFixture) uuid.UUID { return uuid.New() }, payload: `{"ready":true}`, wantCode: ws.CodeNotAllowed},
			{name: "already started", status: domain.GameStatusInProgress, caller: func(f *routerFixture) uuid.UUID { return f.guest }, payload: `{"ready":true}`, wantCode: ws.CodeNotAllowed},
			{name: "bad payload", status: domain.GameStatusWaiting, caller: func(f *routerFixture) uuid.UUID { return f.guest }, payload: `"nope"`, wantCode: ws.CodeInvalidPayload},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := newRouterFixture(t, tt.status)
				payload := errorReply(t, f.route(tt.caller(f), ws.TypeClientPlayerReady, tt.payload))
				if payload.Code != tt.wantCode {
					t.Errorf("error code = %q, want %q", payload.Code, tt.wantCode)
				}
				if got := f.hub.types(); len(got) != 0 {
					t.Errorf("broadcast %v after a refusal, want none", got)
				}
			})
		}
	})

	t.Run("an unavailable store is reported as such", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusWaiting)
		f.state.err = domain.ErrStateUnavailable

		payload := errorReply(t, f.route(f.guest, ws.TypeClientPlayerReady, `{"ready":true}`))
		if payload.Code != ws.CodeStateUnavailable {
			t.Errorf("error code = %q, want %q", payload.Code, ws.CodeStateUnavailable)
		}
	})
}

func TestWSRouterPromptTyping(t *testing.T) {
	f := newRouterFixture(t, domain.GameStatusInProgress)

	if replies := f.route(f.guest, ws.TypeClientPromptTyping, `{}`); len(replies) != 0 {
		t.Fatalf("replies = %+v, want none", replies)
	}
	if got := f.hub.types(); !slices.Equal(got, []ws.Type{ws.TypePlayerTyping}) {
		t.Fatalf("broadcast = %v, want one player_typing", got)
	}
	if !slices.Equal(f.hub.excluded, []uuid.UUID{f.guest}) {
		t.Errorf("excluded = %v, want the sender", f.hub.excluded)
	}
	// Presence carries no prompt text.
	if raw := string(f.hub.broadcast[0].Bytes()); strings.Contains(raw, "prompt") {
		t.Errorf("player_typing payload mentions a prompt: %s", raw)
	}
}

func TestWSRouterUnsupportedAndUnknownEvents(t *testing.T) {
	f := newRouterFixture(t, domain.GameStatusInProgress)

	tests := map[ws.Type]string{
		ws.TypeClientPromptTest:   ws.CodeUnsupportedEvent,
		ws.TypeClientSubmitPrompt: ws.CodeUnsupportedEvent,
		"who_knows":               ws.CodeUnknownEvent,
	}
	for eventType, wantCode := range tests {
		payload := errorReply(t, f.route(f.guest, eventType, `{"prompt":"secret text"}`))
		if payload.Code != wantCode {
			t.Errorf("%s: error code = %q, want %q", eventType, payload.Code, wantCode)
		}
	}
	if got := f.hub.types(); len(got) != 0 {
		t.Errorf("broadcast %v, want none", got)
	}
}
