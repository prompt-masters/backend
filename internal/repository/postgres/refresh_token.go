package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type RefreshTokenRepository struct {
	queries *db.Queries
}

func NewRefreshTokenRepository(queries *db.Queries) *RefreshTokenRepository {
	return &RefreshTokenRepository{queries: queries}
}

func (r *RefreshTokenRepository) Create(ctx context.Context, tokenHash string, userID uuid.UUID, expiresAt time.Time) error {
	return r.queries.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{
		TokenHash: tokenHash,
		UserID:    pgUUID(userID),
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
}

func (r *RefreshTokenRepository) GetActiveUserID(ctx context.Context, tokenHash string) (uuid.UUID, error) {
	id, err := r.queries.GetActiveRefreshTokenUserID(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrInvalidRefreshToken
	}
	if err != nil {
		return uuid.Nil, err
	}
	return uuidFromPg(id), nil
}

func (r *RefreshTokenRepository) Revoke(ctx context.Context, tokenHash string) error {
	return r.queries.RevokeRefreshToken(ctx, tokenHash)
}

func (r *RefreshTokenRepository) Rotate(ctx context.Context, tokenHash, newTokenHash string, expiresAt time.Time) (uuid.UUID, error) {
	id, err := r.queries.RotateRefreshToken(ctx, db.RotateRefreshTokenParams{
		TokenHash:   tokenHash,
		TokenHash_2: newTokenHash,
		ExpiresAt:   pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, domain.ErrInvalidRefreshToken
	}
	if err != nil {
		return uuid.Nil, err
	}
	return uuidFromPg(id), nil
}
