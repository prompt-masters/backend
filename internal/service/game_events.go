package service

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/ws"
)

// EventBroadcaster delivers events to a game's room. *ws.Hub satisfies it.
type EventBroadcaster interface {
	Broadcast(gameID uuid.UUID, msg ws.Message)
	SendToUser(gameID, userID uuid.UUID, msg ws.Message)
	BroadcastExcept(gameID, userID uuid.UUID, msg ws.Message)
}

// GameEvents publishes lobby changes to the game's room, Observer-style: the
// services decide what to send, the hub only delivers it. Publishing is best
// effort and never fails a committed change.
type GameEvents struct {
	hub    EventBroadcaster
	logger *log.Logger
	// now stamps game_state with the server time clients sync their
	// countdown to.
	now func() time.Time
}

func NewGameEvents(hub EventBroadcaster, logger *log.Logger, now func() time.Time) *GameEvents {
	return &GameEvents{hub: hub, logger: logger, now: now}
}

var _ GameEventPublisher = (*GameEvents)(nil)

func (e *GameEvents) PlayerJoined(_ context.Context, game *domain.Game, userID uuid.UUID) {
	e.publish(game.ID, ws.PlayerJoinedFrom(game, userID))
	e.publish(game.ID, ws.GameStateFromGame(game, e.now()))
}

func (e *GameEvents) PlayerLeft(_ context.Context, game *domain.Game, userID uuid.UUID, username string, hostChanged bool) {
	e.publish(game.ID, ws.PlayerLeftFrom(game, userID, username, hostChanged))
	e.publish(game.ID, ws.GameStateFromGame(game, e.now()))
}

func (e *GameEvents) GameUpdated(_ context.Context, game *domain.Game) {
	e.publish(game.ID, ws.GameStateFromGame(game, e.now()))
}

func (e *GameEvents) PlayerSubmitted(_ context.Context, game *domain.Game, userID uuid.UUID, submittedAt time.Time) {
	e.publish(game.ID, ws.PlayerSubmittedPayload{
		UserID:      userID.String(),
		RoundNumber: game.Progress.CurrentRound,
		SubmittedAt: submittedAt.UTC(),
	})
}

func (e *GameEvents) RoundEnded(_ context.Context, game *domain.Game, reason string) {
	e.publish(game.ID, ws.RoundEndPayload{
		RoundNumber: game.Progress.CurrentRound,
		RoundID:     game.Progress.RoundID.String(),
		Reason:      reason,
	})
}

func (e *GameEvents) publish(gameID uuid.UUID, payload ws.Payload) {
	msg, err := ws.Encode(payload)
	if err != nil {
		e.logger.Printf("game_events op=encode event=%s game_id=%s error=%q", payload.EventType(), gameID, err)
		return
	}
	e.hub.Broadcast(gameID, msg)
}
