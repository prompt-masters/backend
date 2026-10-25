package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// SubmissionRepository stores final submissions. There is deliberately no way
// to update or delete one: a submission is locked when it is written, and the
// database refuses updates too.
type SubmissionRepository interface {
	// Create inserts s and sets its ID. It reports false, writing nothing,
	// when the player already has a submission for the round.
	Create(ctx context.Context, s *domain.Submission) (bool, error)
	// Get returns domain.ErrNotFound for an unknown submission.
	Get(ctx context.Context, id uuid.UUID) (*domain.Submission, error)
	// GetForPlayer returns domain.ErrNotFound when the player has none.
	GetForPlayer(ctx context.Context, roundID, playerID uuid.UUID) (*domain.Submission, error)
	Count(ctx context.Context, roundID uuid.UUID) (int, error)
	// ListByRound returns the round's submissions, earliest first.
	ListByRound(ctx context.Context, roundID uuid.UUID) ([]domain.Submission, error)
}
