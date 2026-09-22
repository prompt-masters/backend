package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/ws"
)

// WSRouter handles client messages arriving over WebSocket. The hub decides
// nothing: it hands messages here, and this decides what they mean.
//
// prompt_test and submit_prompt are answered with an unsupported_event error
// until the round and submission services exist.
type WSRouter struct {
	live   *LiveStateService
	hub    EventBroadcaster
	logger *log.Logger
}

func NewWSRouter(live *LiveStateService, hub EventBroadcaster, logger *log.Logger) *WSRouter {
	return &WSRouter{live: live, hub: hub, logger: logger}
}

var _ ws.Router = (*WSRouter)(nil)

func (r *WSRouter) Route(ctx context.Context, in ws.Inbound) {
	switch in.Type {
	case ws.TypeClientPlayerReady:
		r.playerReady(ctx, in)
	case ws.TypeClientPromptTyping:
		r.promptTyping(in)
	case ws.TypeClientPromptTest, ws.TypeClientSubmitPrompt:
		in.Reply(ws.ErrorPayload{
			Code:    ws.CodeUnsupportedEvent,
			Message: fmt.Sprintf("%q is not available yet.", in.Type),
		})
	default:
		in.Reply(ws.ErrorPayload{
			Code:    ws.CodeUnknownEvent,
			Message: fmt.Sprintf("Unknown event %q.", in.Type),
		})
	}
}

func (r *WSRouter) playerReady(ctx context.Context, in ws.Inbound) {
	ready := true
	if len(in.Payload) > 0 {
		var payload ws.ClientPlayerReady
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			in.Reply(ws.ErrorPayload{Code: ws.CodeInvalidPayload, Message: "player_ready payload must be an object."})
			return
		}
		if payload.Ready != nil {
			ready = *payload.Ready
		}
	}

	if err := r.live.SetReady(ctx, in.UserID, in.GameID.String(), ready); err != nil {
		in.Reply(r.errorFor(err, in))
		return
	}

	msg, err := ws.Encode(ws.PlayerReadyPayload{UserID: in.UserID.String(), Ready: ready})
	if err != nil {
		r.logger.Printf("ws_router op=encode event=player_ready error=%q", err)
		return
	}
	r.hub.Broadcast(in.GameID, msg)
}

// promptTyping is presence only: it never carries prompt text and is not
// echoed back to its sender.
func (r *WSRouter) promptTyping(in ws.Inbound) {
	msg, err := ws.Encode(ws.PlayerTypingPayload{UserID: in.UserID.String()})
	if err != nil {
		r.logger.Printf("ws_router op=encode event=player_typing error=%q", err)
		return
	}
	r.hub.BroadcastExcept(in.GameID, in.UserID, msg)
}

// errorFor turns a service error into an error event, keeping internal detail
// out of the client's view.
func (r *WSRouter) errorFor(err error, in ws.Inbound) ws.ErrorPayload {
	var vErr *ValidationError
	switch {
	case errors.As(err, &vErr):
		return ws.ErrorPayload{Code: ws.CodeInvalidPayload, Message: "The submitted values are not valid."}
	case errors.Is(err, domain.ErrNotGamePlayer):
		return ws.ErrorPayload{Code: ws.CodeNotAllowed, Message: "Only players of this game can do this."}
	case errors.Is(err, domain.ErrGameNotWaiting):
		return ws.ErrorPayload{Code: ws.CodeNotAllowed, Message: "This game has already started or ended."}
	case errors.Is(err, domain.ErrGameNotInProgress):
		return ws.ErrorPayload{Code: ws.CodeNotAllowed, Message: "This game is not in progress."}
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrStateNotFound):
		return ws.ErrorPayload{Code: ws.CodeNotAllowed, Message: "The game was not found."}
	case errors.Is(err, domain.ErrStateUnavailable):
		return ws.ErrorPayload{Code: ws.CodeStateUnavailable, Message: "Live game state is temporarily unavailable. Please retry shortly."}
	default:
		r.logger.Printf("ws_router op=route event=%s game_id=%s user_id=%s error=%q", in.Type, in.GameID, in.UserID, err)
		return ws.ErrorPayload{Code: ws.CodeInternal, Message: "An unexpected error occurred."}
	}
}
