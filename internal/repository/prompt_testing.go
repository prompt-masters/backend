package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// PromptTestRepository stores prompt tests. Callers allocating a number hold
// the game's row lock; see NextNumber.
type PromptTestRepository interface {
	// NextNumber returns the player's next test number in the round. It is
	// only safe while the game row is locked.
	NextNumber(ctx context.Context, roundID, playerID uuid.UUID) (int, error)
	// CountUnfinished counts the player's pending or streaming tests in the
	// round created at or after since.
	CountUnfinished(ctx context.Context, roundID, playerID uuid.UUID, since time.Time) (int, error)
	// Create inserts a pending test and sets its ID.
	Create(ctx context.Context, test *domain.PromptTest) error
	// MarkStreaming moves a pending test to streaming.
	MarkStreaming(ctx context.Context, id uuid.UUID, startedAt time.Time) error
	// Finish stores how an unfinished test ended; it returns
	// domain.ErrNotFound when the test has already finished.
	Finish(ctx context.Context, id uuid.UUID, result domain.PromptTestResult) error
	// ListByRoundAndPlayer returns the player's tests in the round by number.
	ListByRoundAndPlayer(ctx context.Context, roundID, playerID uuid.UUID) ([]domain.PromptTest, error)
	// FailStale fails every unfinished test created before before, returning
	// how many there were.
	FailStale(ctx context.Context, before, now time.Time) (int64, error)
	// Get returns domain.ErrNotFound for an unknown test.
	Get(ctx context.Context, id uuid.UUID) (*domain.PromptTest, error)
	// Count counts the player's tests in the round, whatever their status.
	Count(ctx context.Context, roundID, playerID uuid.UUID) (int, error)
	// LastCompleted returns the player's completed test with the highest
	// number, or domain.ErrNotFound.
	LastCompleted(ctx context.Context, roundID, playerID uuid.UUID) (*domain.PromptTest, error)
}
