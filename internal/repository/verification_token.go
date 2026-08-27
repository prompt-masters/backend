package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type VerificationTokenRepository interface {
	Create(ctx context.Context, userID uuid.UUID, token string, expiresAt time.Time) (*domain.VerificationToken, error)
	GetByToken(ctx context.Context, token string) (*domain.VerificationToken, error)
	DeleteByToken(ctx context.Context, token string) error
	DeleteExpired(ctx context.Context) error
}
