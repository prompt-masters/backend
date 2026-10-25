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

type JudgingRepository struct {
	queries *db.Queries
}

// NewJudgingRepository binds the repository to queries, which may run on a
// pool or inside a transaction.
func NewJudgingRepository(queries *db.Queries) *JudgingRepository {
	return &JudgingRepository{queries: queries}
}

func (r *JudgingRepository) Claim(ctx context.Context, roundID, gameID uuid.UUID, now, staleBefore time.Time) (int, bool, error) {
	attempt, err := r.queries.InsertJudgingRun(ctx, db.InsertJudgingRunParams{
		RoundID: pgUUID(roundID), GameID: pgUUID(gameID), StartedAtMs: now.UnixMilli(),
	})
	if err == nil {
		return int(attempt), true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	attempt, err = r.queries.ReclaimJudgingRun(ctx, db.ReclaimJudgingRunParams{
		RoundID: pgUUID(roundID), NowMs: now.UnixMilli(), StaleBeforeMs: staleBefore.UnixMilli(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(attempt), true, nil
}

func (r *JudgingRepository) Get(ctx context.Context, roundID uuid.UUID) (*domain.JudgingRun, error) {
	row, err := r.queries.GetJudgingRun(ctx, pgUUID(roundID))
	if err != nil {
		return nil, translateGetError(err)
	}
	return &domain.JudgingRun{
		RoundID:   uuidFromPg(row.RoundID),
		GameID:    uuidFromPg(row.GameID),
		Status:    domain.JudgingRunStatus(row.Status),
		Attempt:   int(row.Attempt),
		StartedAt: time.UnixMilli(row.StartedAtMs).UTC(),
		ErrorCode: row.ErrorCode.String,
	}, nil
}

func (r *JudgingRepository) Complete(ctx context.Context, roundID uuid.UUID, attempt int, at time.Time, call domain.JudgeCall) error {
	in, out := call.InputTokens, call.OutputTokens
	n, err := r.queries.CompleteJudgingRun(ctx, db.CompleteJudgingRunParams{
		RoundID:      pgUUID(roundID),
		Attempt:      int32(attempt),
		FinishedAtMs: pgtype.Int8{Int64: at.UnixMilli(), Valid: true},
		Provider:     pgOptionalText(call.Provider),
		Model:        pgOptionalText(call.Model),
		InputTokens:  pgInt4(&in),
		OutputTokens: pgInt4(&out),
		CostUsd:      pgFloat8(call.CostUSD),
	})
	if err == nil && n == 0 {
		return domain.ErrStaleGameState
	}
	return err
}

func (r *JudgingRepository) Fail(ctx context.Context, roundID uuid.UUID, attempt int, at time.Time, errorCode string) error {
	n, err := r.queries.FailJudgingRun(ctx, db.FailJudgingRunParams{
		RoundID:      pgUUID(roundID),
		Attempt:      int32(attempt),
		FinishedAtMs: pgtype.Int8{Int64: at.UnixMilli(), Valid: true},
		ErrorCode:    pgOptionalText(errorCode),
	})
	if err == nil && n == 0 {
		return domain.ErrStaleGameState
	}
	return err
}

func (r *JudgingRepository) CreateScore(ctx context.Context, s domain.Score) error {
	return r.queries.CreateScore(ctx, db.CreateScoreParams{
		GameID:       pgUUID(s.GameID),
		RoundID:      pgUUID(s.RoundID),
		PlayerID:     pgUUID(s.PlayerID),
		SubmissionID: pgOptionalUUID(s.SubmissionID),
		NoSubmission: s.NoSubmission,
		Label:        pgOptionalText(s.Label),
		Accuracy:     int32(s.Components.Accuracy),
		Simplicity:   int32(s.Components.Simplicity),
		Creativity:   int32(s.Components.Creativity),
		Adherence:    int32(s.Components.Adherence),
		Efficiency:   int32(s.Components.Efficiency),
		Total:        int32(s.Total),
		Rank:         int32(s.Rank),
		Rationale:    pgOptionalText(s.Rationale),
		Provider:     pgOptionalText(s.Provider),
		Model:        pgOptionalText(s.Model),
		CreatedAtMs:  s.CreatedAt.UnixMilli(),
	})
}

func (r *JudgingRepository) ListScores(ctx context.Context, roundID uuid.UUID) ([]domain.Score, error) {
	rows, err := r.queries.ListScores(ctx, pgUUID(roundID))
	if err != nil {
		return nil, err
	}
	scores := make([]domain.Score, len(rows))
	for i, row := range rows {
		scores[i] = domain.Score{
			GameID:       uuidFromPg(row.GameID),
			RoundID:      uuidFromPg(row.RoundID),
			PlayerID:     uuidFromPg(row.PlayerID),
			SubmissionID: uuidFromPg(row.SubmissionID),
			NoSubmission: row.NoSubmission,
			Label:        row.Label.String,
			Components: domain.ScoreComponents{
				Accuracy:   domain.Points(row.Accuracy),
				Simplicity: domain.Points(row.Simplicity),
				Creativity: domain.Points(row.Creativity),
				Adherence:  domain.Points(row.Adherence),
				Efficiency: domain.Points(row.Efficiency),
			},
			Total:     domain.Points(row.Total),
			Rank:      int(row.Rank),
			Rationale: row.Rationale.String,
			Provider:  row.Provider.String,
			Model:     row.Model.String,
			CreatedAt: time.UnixMilli(row.CreatedAtMs).UTC(),
		}
	}
	return scores, nil
}
