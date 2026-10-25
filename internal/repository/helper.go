package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// HelperRepository stores helper balances and usage.
type HelperRepository interface {
	// EnsureBalance gives the player a balance in the game if they have none.
	EnsureBalance(ctx context.Context, gameID, playerID uuid.UUID, balance int, now time.Time) error
	// GetBalance returns domain.ErrNotFound when the player has no balance.
	GetBalance(ctx context.Context, gameID, playerID uuid.UUID) (int, error)
	// Debit takes cost if the balance covers it and returns what is left; it
	// reports false, taking nothing, when it does not.
	Debit(ctx context.Context, gameID, playerID uuid.UUID, cost int, now time.Time) (int, bool, error)
	// Credit adds amount and returns the new balance.
	Credit(ctx context.Context, gameID, playerID uuid.UUID, amount int, now time.Time) (int, error)

	// CreateUsage inserts a reserved usage and sets its ID. It reports false,
	// writing nothing, when the player already used the idempotency key.
	CreateUsage(ctx context.Context, u *domain.HelperUsage) (bool, error)
	// GetUsage and GetUsageByKey return domain.ErrNotFound when missing.
	GetUsage(ctx context.Context, id uuid.UUID) (*domain.HelperUsage, error)
	GetUsageByKey(ctx context.Context, playerID uuid.UUID, key string) (*domain.HelperUsage, error)
	// Deliver marks a reserved usage delivered; it returns
	// domain.ErrStaleGameState when the usage is no longer reserved.
	Deliver(ctx context.Context, id uuid.UUID, d domain.HelperDelivery) error
	// MarkRefunded marks a reserved usage refunded and reports whether it did:
	// only the first refund of a reservation succeeds.
	MarkRefunded(ctx context.Context, id uuid.UUID, errorCode string, at time.Time) (bool, error)
	// ListUsage returns the player's usage in the round, oldest first.
	ListUsage(ctx context.Context, roundID, playerID uuid.UUID) ([]domain.HelperUsage, error)
	// ListStaleReservations returns reservations created before before.
	ListStaleReservations(ctx context.Context, before time.Time) ([]domain.HelperUsage, error)
}
