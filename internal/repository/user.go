package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type UserRepository interface {
	Create(ctx context.Context, email, plainPassword string) (*domain.User, error)
	GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error)
	GetByEmail(ctx context.Context, email string) (*domain.User, error)
	UpdateEmail(ctx context.Context, id uuid.UUID, email string) error
	SetEmailVerified(ctx context.Context, id uuid.UUID) error
}
