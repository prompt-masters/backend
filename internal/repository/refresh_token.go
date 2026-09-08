package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type RefreshTokenRepository interface {
	Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error
	GetActiveUserID(ctx context.Context, tokenHash string) (uuid.UUID, error)
	Rotate(ctx context.Context, tokenHash, newTokenHash string, expiresAt time.Time) (uuid.UUID, error)
	Revoke(ctx context.Context, tokenHash string) error
}
