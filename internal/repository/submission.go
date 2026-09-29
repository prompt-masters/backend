package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type SubmissionRepository interface {
	// Create returns domain.ErrAlreadySubmitted when the user has already
	// submitted in the round.
	Create(ctx context.Context, s domain.Submission) error
	CountByRound(ctx context.Context, roundID uuid.UUID) (int, error)
	// ListByRound returns the round's submissions, earliest first.
	ListByRound(ctx context.Context, roundID uuid.UUID) ([]domain.Submission, error)
}
