package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (r *SubmissionRepository) Create(ctx context.Context, s *domain.Submission) (bool, error) {
	row, err := r.queries.CreateSubmission(ctx, db.CreateSubmissionParams{
		GameID:        pgUUID(s.GameID),
		RoundID:       pgUUID(s.RoundID),
		PlayerID:      pgUUID(s.PlayerID),
		TestID:        pgUUID(s.TestID),
		Prompt:        s.Prompt,
		Output:        s.Output,
		PromptLength:  int32(s.PromptLength),
		TestCount:     int32(s.TestCount),
		Source:        string(s.Source),
		SubmittedAtMs: s.SubmittedAt.UnixMilli(),
		LockedAtMs:    s.LockedAt.UnixMilli(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // ON CONFLICT DO NOTHING: the first submission stays
	}
	if err != nil {
		return false, err
	}
	s.ID = uuidFromPg(row.ID)
	return true, nil
}

func (r *SubmissionRepository) Get(ctx context.Context, id uuid.UUID) (*domain.Submission, error) {
	row, err := r.queries.GetSubmission(ctx, pgUUID(id))
	if err != nil {
		return nil, translateGetError(err)
	}
	s := toDomainSubmission(row)
	return &s, nil
}

func (r *SubmissionRepository) GetForPlayer(ctx context.Context, roundID, playerID uuid.UUID) (*domain.Submission, error) {
	row, err := r.queries.GetPlayerSubmission(ctx, db.GetPlayerSubmissionParams{RoundID: pgUUID(roundID), PlayerID: pgUUID(playerID)})
	if err != nil {
		return nil, translateGetError(err)
	}
	s := toDomainSubmission(row)
	return &s, nil
}

func (r *SubmissionRepository) Count(ctx context.Context, roundID uuid.UUID) (int, error) {
	n, err := r.queries.CountSubmissions(ctx, pgUUID(roundID))
	return int(n), err
}

func (r *SubmissionRepository) ListByRound(ctx context.Context, roundID uuid.UUID) ([]domain.Submission, error) {
	rows, err := r.queries.ListSubmissions(ctx, pgUUID(roundID))
	if err != nil {
		return nil, err
	}
	subs := make([]domain.Submission, len(rows))
	for i, row := range rows {
		subs[i] = toDomainSubmission(row)
	}
	return subs, nil
}

func toDomainSubmission(row *db.Submission) domain.Submission {
	return domain.Submission{
		ID:           uuidFromPg(row.ID),
		GameID:       uuidFromPg(row.GameID),
		RoundID:      uuidFromPg(row.RoundID),
		PlayerID:     uuidFromPg(row.PlayerID),
		TestID:       uuidFromPg(row.TestID),
		Prompt:       row.Prompt,
		Output:       row.Output,
		PromptLength: int(row.PromptLength),
		TestCount:    int(row.TestCount),
		Source:       domain.SubmissionSource(row.Source),
		SubmittedAt:  time.UnixMilli(row.SubmittedAtMs).UTC(),
		LockedAt:     time.UnixMilli(row.LockedAtMs).UTC(),
	}
}
