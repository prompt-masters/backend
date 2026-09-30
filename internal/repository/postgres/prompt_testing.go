package postgres

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type PromptTestRepository struct {
	queries *db.Queries
}

// NewPromptTestRepository binds the repository to queries, which may run on
// a pool or inside a transaction.
func NewPromptTestRepository(queries *db.Queries) *PromptTestRepository {
	return &PromptTestRepository{queries: queries}
}

func (r *PromptTestRepository) NextNumber(ctx context.Context, roundID, playerID uuid.UUID) (int, error) {
	n, err := r.queries.NextPromptTestNumber(ctx, db.NextPromptTestNumberParams{RoundID: pgUUID(roundID), PlayerID: pgUUID(playerID)})
	return int(n), err
}

func (r *PromptTestRepository) CountUnfinished(ctx context.Context, roundID, playerID uuid.UUID, since time.Time) (int, error) {
	n, err := r.queries.CountUnfinishedPromptTests(ctx, db.CountUnfinishedPromptTestsParams{
		RoundID: pgUUID(roundID), PlayerID: pgUUID(playerID), SinceMs: since.UnixMilli(),
	})
	return int(n), err
}

func (r *PromptTestRepository) Create(ctx context.Context, t *domain.PromptTest) error {
	id, err := r.queries.CreatePromptTest(ctx, db.CreatePromptTestParams{
		GameID:      pgUUID(t.GameID),
		RoundID:     pgUUID(t.RoundID),
		PlayerID:    pgUUID(t.PlayerID),
		TestNumber:  int32(t.TestNumber),
		Prompt:      t.Prompt,
		CreatedAtMs: t.CreatedAt.UnixMilli(),
	})
	if err != nil {
		return err
	}
	t.ID = uuidFromPg(id)
	return nil
}

func (r *PromptTestRepository) MarkStreaming(ctx context.Context, id uuid.UUID, startedAt time.Time) error {
	n, err := r.queries.MarkPromptTestStreaming(ctx, db.MarkPromptTestStreamingParams{
		ID: pgUUID(id), StartedAtMs: pgtype.Int8{Int64: startedAt.UnixMilli(), Valid: true},
	})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *PromptTestRepository) Finish(ctx context.Context, id uuid.UUID, res domain.PromptTestResult) error {
	n, err := r.queries.FinishPromptTest(ctx, db.FinishPromptTestParams{
		ID:            pgUUID(id),
		Status:        string(res.Status),
		Output:        pgText(res.Output),
		ErrorCode:     pgOptionalText(res.ErrorCode),
		Provider:      pgOptionalText(res.Provider),
		Model:         pgOptionalText(res.Model),
		InputTokens:   pgInt4(res.InputTokens),
		OutputTokens:  pgInt4(res.OutputTokens),
		CostUsd:       pgFloat8(res.CostUSD),
		LatencyMs:     pgInt8(res.LatencyMs),
		CompletedAtMs: pgtype.Int8{Int64: res.CompletedAt.UnixMilli(), Valid: true},
	})
	if err == nil && n == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *PromptTestRepository) ListByRoundAndPlayer(ctx context.Context, roundID, playerID uuid.UUID) ([]domain.PromptTest, error) {
	rows, err := r.queries.ListPromptTests(ctx, db.ListPromptTestsParams{RoundID: pgUUID(roundID), PlayerID: pgUUID(playerID)})
	if err != nil {
		return nil, err
	}
	tests := make([]domain.PromptTest, len(rows))
	for i, row := range rows {
		tests[i] = toDomainPromptTest(row)
	}
	return tests, nil
}

func (r *PromptTestRepository) FailStale(ctx context.Context, before, now time.Time) (int64, error) {
	return r.queries.FailStalePromptTests(ctx, db.FailStalePromptTestsParams{BeforeMs: before.UnixMilli(), NowMs: pgtype.Int8{Int64: now.UnixMilli(), Valid: true}})
}

func toDomainPromptTest(row *db.PromptTest) domain.PromptTest {
	t := domain.PromptTest{
		ID:           uuidFromPg(row.ID),
		GameID:       uuidFromPg(row.GameID),
		RoundID:      uuidFromPg(row.RoundID),
		PlayerID:     uuidFromPg(row.PlayerID),
		TestNumber:   int(row.TestNumber),
		Prompt:       row.Prompt,
		Status:       domain.PromptTestStatus(row.Status),
		ErrorCode:    row.ErrorCode.String,
		Provider:     row.Provider.String,
		Model:        row.Model.String,
		InputTokens:  intFromPg4(row.InputTokens),
		OutputTokens: intFromPg4(row.OutputTokens),
		CreatedAt:    time.UnixMilli(row.CreatedAtMs).UTC(),
		StartedAt:    timeFromMs(row.StartedAtMs),
		CompletedAt:  timeFromMs(row.CompletedAtMs),
	}
	if row.Output.Valid {
		t.Output = &row.Output.String
	}
	if row.CostUsd.Valid {
		t.CostUSD = &row.CostUsd.Float64
	}
	if row.LatencyMs.Valid {
		t.LatencyMs = &row.LatencyMs.Int64
	}
	return t
}

func pgText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func pgOptionalText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}

func pgInt4(v *int) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

func pgInt8(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func pgFloat8(v *float64) pgtype.Float8 {
	if v == nil {
		return pgtype.Float8{}
	}
	return pgtype.Float8{Float64: *v, Valid: true}
}

func intFromPg4(v pgtype.Int4) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int32)
	return &n
}

func timeFromMs(v pgtype.Int8) *time.Time {
	if !v.Valid {
		return nil
	}
	t := time.UnixMilli(v.Int64).UTC()
	return &t
}
