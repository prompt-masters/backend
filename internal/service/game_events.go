package service

import (
	"context"
	"log"

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
}

func NewGameEvents(hub EventBroadcaster, logger *log.Logger) *GameEvents {
	return &GameEvents{hub: hub, logger: logger}
}

var _ GameEventPublisher = (*GameEvents)(nil)

func (e *GameEvents) PlayerJoined(_ context.Context, game *domain.Game, userID uuid.UUID) {
	e.publish(game.ID, ws.PlayerJoinedFrom(game, userID))
	e.publish(game.ID, ws.GameStateFromGame(game))
}

func (e *GameEvents) PlayerLeft(_ context.Context, game *domain.Game, userID uuid.UUID, username string, hostChanged bool) {
	e.publish(game.ID, ws.PlayerLeftFrom(game, userID, username, hostChanged))
	e.publish(game.ID, ws.GameStateFromGame(game))
}

func (e *GameEvents) GameUpdated(_ context.Context, game *domain.Game) {
	e.publish(game.ID, ws.GameStateFromGame(game))
}

func (e *GameEvents) publish(gameID uuid.UUID, payload ws.Payload) {
	msg, err := ws.Encode(payload)
	if err != nil {
		e.logger.Printf("game_events op=encode event=%s game_id=%s error=%q", payload.EventType(), gameID, err)
		return
	}
	e.hub.Broadcast(gameID, msg)
}
