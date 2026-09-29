package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type SubmissionRepository struct {
	queries *db.Queries
}

// NewSubmissionRepository binds the repository to queries, which may run on a
// pool or inside a transaction.
func NewSubmissionRepository(queries *db.Queries) *SubmissionRepository {
	return &SubmissionRepository{queries: queries}
}

func (r *SubmissionRepository) Create(ctx context.Context, s domain.Submission) error {
	err := r.queries.CreateRoundSubmission(ctx, db.CreateRoundSubmissionParams{
		RoundID:       pgUUID(s.RoundID),
		GameID:        pgUUID(s.GameID),
		RoundNumber:   int16(s.RoundNumber),
		UserID:        pgUUID(s.UserID),
		Prompt:        s.Prompt,
		SubmittedAtMs: s.SubmittedAt.UnixMilli(),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == roundSubmissionsPrimaryKey {
		return domain.ErrAlreadySubmitted
	}
	return err
}

func (r *SubmissionRepository) CountByRound(ctx context.Context, roundID uuid.UUID) (int, error) {
	n, err := r.queries.CountRoundSubmissions(ctx, pgUUID(roundID))
	return int(n), err
}
