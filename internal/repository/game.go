package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type GameRepository interface {
	// Create inserts a waiting game. It returns domain.ErrRoomCodeTaken when
	// another active game holds the room code; the transaction stays usable.
	Create(ctx context.Context, hostID uuid.UUID, roomCode string) (*domain.Game, error)
	CreateSettings(ctx context.Context, gameID uuid.UUID, settings domain.GameSettings) error
	UpdateSettings(ctx context.Context, gameID uuid.UUID, settings domain.GameSettings) error

	// GetByID loads the game with its settings and players.
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Game, error)
	// LockByID is GetByID that also row-locks the game until the surrounding
	// transaction ends, serializing every state and membership change.
	LockByID(ctx context.Context, id uuid.UUID) (*domain.Game, error)
	GetActiveIDByRoomCode(ctx context.Context, roomCode string) (uuid.UUID, error)
	// ListByStatus returns games newest first, with settings and player
	// counts but without players.
	ListByStatus(ctx context.Context, status domain.GameStatus, limit int) ([]*domain.Game, error)

	// AddPlayer returns domain.ErrAlreadyJoined when the user is a player.
	AddPlayer(ctx context.Context, gameID, userID uuid.UUID) error
	// RemovePlayer returns domain.ErrNotInGame when the user is not a player.
	RemovePlayer(ctx context.Context, gameID, userID uuid.UUID) error
	UpdateHost(ctx context.Context, gameID, hostID uuid.UUID) error
	UpdateStatus(ctx context.Context, gameID uuid.UUID, status domain.GameStatus) error
}

// GameTransactor runs fn in a single database transaction, handing it a
// GameRepository bound to that transaction. The transaction commits when fn
// returns nil and rolls back otherwise.
type GameTransactor interface {
	WithinTx(ctx context.Context, fn func(games GameRepository) error) error
}
