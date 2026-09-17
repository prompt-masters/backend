package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type ChallengeRepository interface {
	List(ctx context.Context, filter domain.ChallengeFilter) ([]*domain.Challenge, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Challenge, error)
}
