package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// GameStateRepository stores live game state. Every method is scoped to one
// game. Failures to reach the store are reported as domain.ErrStateUnavailable
// and missing state as domain.ErrStateNotFound.
//
// Every write refreshes the expiry of all of the game's keys: a sliding TTL
// while the game is waiting or in progress, and a short one once the stored
// lobby status is finished or cancelled.
type GameStateRepository interface {
	SetLobby(ctx context.Context, lobby domain.LobbyState) error
	GetLobby(ctx context.Context, gameID uuid.UUID) (*domain.LobbyState, error)
	// SyncLobby replaces the lobby state and roster in one step. Players missing
	// from the roster lose their readiness and draft; points are kept.
	SyncLobby(ctx context.Context, lobby domain.LobbyState, players []domain.LivePlayer) error

	AddPlayer(ctx context.Context, gameID uuid.UUID, player domain.LivePlayer) error
	// RemovePlayer also clears the player's readiness and draft.
	RemovePlayer(ctx context.Context, gameID, userID uuid.UUID) error
	// ListPlayers returns the roster ordered by join time.
	ListPlayers(ctx context.Context, gameID uuid.UUID) ([]domain.LivePlayer, error)

	// SetReady and UnsetReady return domain.ErrNotGamePlayer for non-players.
	SetReady(ctx context.Context, gameID, userID uuid.UUID) error
	UnsetReady(ctx context.Context, gameID, userID uuid.UUID) error
	ListReady(ctx context.Context, gameID uuid.UUID) ([]uuid.UUID, error)

	SetRound(ctx context.Context, gameID uuid.UUID, round domain.RoundState) error
	GetRound(ctx context.Context, gameID uuid.UUID) (*domain.RoundState, error)

	// SaveDraft returns domain.ErrNotGamePlayer for non-players.
	SaveDraft(ctx context.Context, gameID, userID uuid.UUID, draft domain.Draft) error
	GetDraft(ctx context.Context, gameID, userID uuid.UUID) (*domain.Draft, error)

	// IncrementPoints atomically adds delta and returns the new total. It
	// returns domain.ErrNotGamePlayer for non-players.
	IncrementPoints(ctx context.Context, gameID, userID uuid.UUID, delta int64) (int64, error)
	GetPoints(ctx context.Context, gameID uuid.UUID) (map[uuid.UUID]int64, error)

	// Load reads the whole live state of a game in one consistent snapshot,
	// including only viewerID's draft. RoundRemaining is left for the caller to
	// compute.
	Load(ctx context.Context, gameID, viewerID uuid.UUID) (*domain.LiveGameState, error)
	// Delete removes every key of the game.
	Delete(ctx context.Context, gameID uuid.UUID) error
}
