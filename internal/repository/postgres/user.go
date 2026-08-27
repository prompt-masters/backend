package postgres

import (
	"context"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type UserRepository struct {
	queries *db.Queries
}

func NewUserRepository(queries *db.Queries) *UserRepository {
	return &UserRepository{queries: queries}
}

func (r *UserRepository) Create(ctx context.Context, user *domain.User) (*domain.User, error) {
	id := uuid.New()
	created, err := r.queries.CreateUser(ctx, db.CreateUserParams{
		ID:       pgUUID(id),
		Username: user.Username,
		Email:    user.Email,
		Password: user.Password,
	})
	if err != nil {
		return nil, err
	}
	return toDomainUser(created), nil
}

func (r *UserRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.User, error) {
	user, err := r.queries.GetUserByID(ctx, pgUUID(id))
	if err != nil {
		return nil, err
	}
	return toDomainUser(user), nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	user, err := r.queries.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, err
	}
	return toDomainUser(user), nil
}

func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	user, err := r.queries.GetUserByUsername(ctx, username)
	if err != nil {
		return nil, err
	}
	return toDomainUser(user), nil
}

func (r *UserRepository) UpdateEmail(ctx context.Context, id uuid.UUID, email string) error {
	return r.queries.UpdateUserEmail(ctx, db.UpdateUserEmailParams{
		ID:    pgUUID(id),
		Email: email,
	})
}

func (r *UserRepository) SetEmailVerified(ctx context.Context, id uuid.UUID) error {
	return r.queries.SetEmailVerified(ctx, pgUUID(id))
}

func toDomainUser(u *db.User) *domain.User {
	return &domain.User{
		ID:            uuidFromPg(u.ID),
		Username:      u.Username,
		Email:         u.Email,
		Password:      u.Password,
		EmailVerified: u.EmailVerified,
		CreatedAt:     u.CreatedAt.Time,
		UpdatedAt:     u.UpdatedAt.Time,
	}
}
