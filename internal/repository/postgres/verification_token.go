package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type VerificationTokenRepository struct {
	queries *db.Queries
}

func NewVerificationTokenRepository(queries *db.Queries) *VerificationTokenRepository {
	return &VerificationTokenRepository{queries: queries}
}

func (r *VerificationTokenRepository) Create(ctx context.Context, userID uuid.UUID, token string, expiresAt time.Time) (*domain.VerificationToken, error) {
	created, err := r.queries.CreateVerificationToken(ctx, db.CreateVerificationTokenParams{
		UserID:    pgUUID(userID),
		Token:     token,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return nil, err
	}

	return toDomainToken(created), nil
}

func (r *VerificationTokenRepository) GetByToken(ctx context.Context, token string) (*domain.VerificationToken, error) {
	t, err := r.queries.GetVerificationToken(ctx, token)
	if err != nil {
		return nil, err
	}
	return toDomainToken(t), nil
}

func (r *VerificationTokenRepository) DeleteByToken(ctx context.Context, token string) error {
	return r.queries.DeleteVerificationToken(ctx, token)
}

func (r *VerificationTokenRepository) DeleteExpired(ctx context.Context) error {
	return r.queries.DeleteExpiredTokens(ctx)
}

func toDomainToken(t *db.VerificationToken) *domain.VerificationToken {
	return &domain.VerificationToken{
		ID:        t.ID,
		UserID:    uuidFromPg(t.UserID),
		Token:     t.Token,
		ExpiresAt: t.ExpiresAt.Time,
		CreatedAt: t.CreatedAt.Time,
	}
}
