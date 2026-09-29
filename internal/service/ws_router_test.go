package service

import (
	"context"
	"encoding/json"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
	events  *recordingEvents
	router  *WSRouter
	replies []ws.Payload
}

func newRouterFixture(t *testing.T, status domain.GameStatus) *routerFixture {
	t.Helper()
	live := newLiveFixture(t, status, true)
	hub := &recordingHub{}
	events := &recordingEvents{}
	logger := log.New(live.logs, "", 0)
	engine := NewGameEngine(live.games, newFakeClock(liveNow), events, live.svc, logger)
	t.Cleanup(engine.Close)
	return &routerFixture{liveFixture: live, hub: hub, events: events, router: NewWSRouter(live.svc, engine, hub, logger)}
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
		ws.TypeClientPromptTest: ws.CodeUnsupportedEvent,
		"who_knows":             ws.CodeUnknownEvent,
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

func TestWSRouterSubmitPrompt(t *testing.T) {
	// activeRound puts the fixture's game in round 1, due in a minute.
	activeRound := func(f *routerFixture) uuid.UUID {
		roundID := uuid.New()
		f.games.mu.Lock()
		defer f.games.mu.Unlock()
		f.games.games[f.game.ID].Progress = domain.GameProgress{
			Phase: domain.GamePhaseRoundActive, CurrentRound: 1, RoundID: roundID,
			RoundStartedAt: liveNow.Add(-30 * time.Second), RoundDeadline: liveNow.Add(time.Minute),
		}
		return roundID
	}

	t.Run("records the submission and announces it", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusInProgress)
		roundID := activeRound(f)

		if replies := f.route(f.guest, ws.TypeClientSubmitPrompt, `{"prompt":"secret text","round_id":"`+roundID.String()+`"}`); len(replies) != 0 {
			t.Fatalf("replies = %+v, want none", replies)
		}
		if subs := f.games.submissions[roundID]; len(subs) != 1 || subs[0].UserID != f.guest || subs[0].Prompt != "secret text" {
			t.Errorf("stored submissions = %+v", subs)
		}
		if kinds := f.events.kinds(); !slices.Equal(kinds, []string{"player_submitted"}) {
			t.Errorf("published %v, want player_submitted", kinds)
		}
	})

	t.Run("refusals answer the sender", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusInProgress)
		roundID := activeRound(f)
		if replies := f.route(f.host, ws.TypeClientSubmitPrompt, `{"prompt":"first"}`); len(replies) != 0 {
			t.Fatalf("replies = %+v, want none", replies)
		}

		tests := []struct {
			name     string
			sender   uuid.UUID
			payload  string
			wantCode string
		}{
			{name: "not an object", sender: f.guest, payload: `"hi"`, wantCode: ws.CodeInvalidPayload},
			{name: "malformed round_id", sender: f.guest, payload: `{"prompt":"p","round_id":"nope"}`, wantCode: ws.CodeInvalidPayload},
			{name: "blank prompt", sender: f.guest, payload: `{"prompt":" "}`, wantCode: ws.CodeInvalidPayload},
			{name: "another round", sender: f.guest, payload: `{"prompt":"p","round_id":"` + uuid.NewString() + `"}`, wantCode: ws.CodeNotAllowed},
			{name: "second submission", sender: f.host, payload: `{"prompt":"again"}`, wantCode: ws.CodeNotAllowed},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				payload := errorReply(t, f.route(tt.sender, ws.TypeClientSubmitPrompt, tt.payload))
				if payload.Code != tt.wantCode {
					t.Errorf("error = %+v, want code %q", payload, tt.wantCode)
				}
			})
		}
		if n := len(f.games.submissions[roundID]); n != 1 {
			t.Errorf("stored %d submissions, want only the first", n)
		}
	})

	t.Run("outside an active round", func(t *testing.T) {
		f := newRouterFixture(t, domain.GameStatusInProgress) // seeded in Judging
		payload := errorReply(t, f.route(f.guest, ws.TypeClientSubmitPrompt, `{"prompt":"p"}`))
		if payload.Code != ws.CodeNotAllowed || !strings.Contains(payload.Message, "not accepting submissions") {
			t.Errorf("error = %+v, want not_allowed for a closed round", payload)
		}
	})
}
