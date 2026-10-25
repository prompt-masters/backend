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

type HelperRepository struct {
	queries *db.Queries
}

// NewHelperRepository binds the repository to queries, which may run on a
// pool or inside a transaction.
func NewHelperRepository(queries *db.Queries) *HelperRepository {
	return &HelperRepository{queries: queries}
}

func (r *HelperRepository) EnsureBalance(ctx context.Context, gameID, playerID uuid.UUID, balance int, now time.Time) error {
	return r.queries.EnsureHelperBalance(ctx, db.EnsureHelperBalanceParams{
		GameID: pgUUID(gameID), PlayerID: pgUUID(playerID), Balance: int32(balance), UpdatedAtMs: now.UnixMilli(),
	})
}

func (r *HelperRepository) GetBalance(ctx context.Context, gameID, playerID uuid.UUID) (int, error) {
	b, err := r.queries.GetHelperBalance(ctx, db.GetHelperBalanceParams{GameID: pgUUID(gameID), PlayerID: pgUUID(playerID)})
	if err != nil {
		return 0, translateGetError(err)
	}
	return int(b), nil
}

func (r *HelperRepository) Debit(ctx context.Context, gameID, playerID uuid.UUID, cost int, now time.Time) (int, bool, error) {
	b, err := r.queries.DebitHelperBalance(ctx, db.DebitHelperBalanceParams{
		GameID: pgUUID(gameID), PlayerID: pgUUID(playerID), Cost: int32(cost), NowMs: now.UnixMilli(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return int(b), true, nil
}

func (r *HelperRepository) Credit(ctx context.Context, gameID, playerID uuid.UUID, amount int, now time.Time) (int, error) {
	b, err := r.queries.CreditHelperBalance(ctx, db.CreditHelperBalanceParams{
		GameID: pgUUID(gameID), PlayerID: pgUUID(playerID), Amount: int32(amount), NowMs: now.UnixMilli(),
	})
	if err != nil {
		return 0, translateGetError(err)
	}
	return int(b), nil
}

func (r *HelperRepository) CreateUsage(ctx context.Context, u *domain.HelperUsage) (bool, error) {
	id, err := r.queries.CreateHelperUsage(ctx, db.CreateHelperUsageParams{
		GameID:         pgUUID(u.GameID),
		RoundID:        pgUUID(u.RoundID),
		PlayerID:       pgUUID(u.PlayerID),
		HelperType:     string(u.Type),
		Cost:           int32(u.Cost),
		IdempotencyKey: u.IdempotencyKey,
		InputPrompt:    pgOptionalText(u.InputPrompt),
		TestID:         pgOptionalUUID(u.TestID),
		CreatedAtMs:    u.CreatedAt.UnixMilli(),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	u.ID = uuidFromPg(id)
	return true, nil
}

func (r *HelperRepository) GetUsage(ctx context.Context, id uuid.UUID) (*domain.HelperUsage, error) {
	row, err := r.queries.GetHelperUsage(ctx, pgUUID(id))
	if err != nil {
		return nil, translateGetError(err)
	}
	u := toDomainHelperUsage(row)
	return &u, nil
}

func (r *HelperRepository) GetUsageByKey(ctx context.Context, playerID uuid.UUID, key string) (*domain.HelperUsage, error) {
	row, err := r.queries.GetHelperUsageByKey(ctx, db.GetHelperUsageByKeyParams{PlayerID: pgUUID(playerID), IdempotencyKey: key})
	if err != nil {
		return nil, translateGetError(err)
	}
	u := toDomainHelperUsage(row)
	return &u, nil
}

func (r *HelperRepository) Deliver(ctx context.Context, id uuid.UUID, d domain.HelperDelivery) error {
	in, out := d.InputTokens, d.OutputTokens
	n, err := r.queries.DeliverHelperUsage(ctx, db.DeliverHelperUsageParams{
		ID:            pgUUID(id),
		Content:       pgtype.Text{String: d.Content, Valid: true},
		DeliveredAtMs: pgtype.Int8{Int64: d.DeliveredAt.UnixMilli(), Valid: true},
		Provider:      pgOptionalText(d.Provider),
		Model:         pgOptionalText(d.Model),
		InputTokens:   pgInt4(&in),
		OutputTokens:  pgInt4(&out),
		CostUsd:       pgFloat8(d.CostUSD),
	})
	if err == nil && n == 0 {
		return domain.ErrStaleGameState
	}
	return err
}

func (r *HelperRepository) MarkRefunded(ctx context.Context, id uuid.UUID, errorCode string, at time.Time) (bool, error) {
	n, err := r.queries.RefundHelperUsage(ctx, db.RefundHelperUsageParams{
		ID: pgUUID(id), ErrorCode: pgOptionalText(errorCode), RefundedAtMs: pgtype.Int8{Int64: at.UnixMilli(), Valid: true},
	})
	return n == 1, err
}

func (r *HelperRepository) ListUsage(ctx context.Context, roundID, playerID uuid.UUID) ([]domain.HelperUsage, error) {
	rows, err := r.queries.ListHelperUsage(ctx, db.ListHelperUsageParams{RoundID: pgUUID(roundID), PlayerID: pgUUID(playerID)})
	if err != nil {
		return nil, err
	}
	out := make([]domain.HelperUsage, len(rows))
	for i, row := range rows {
		out[i] = toDomainHelperUsage(row)
	}
	return out, nil
}

func (r *HelperRepository) ListStaleReservations(ctx context.Context, before time.Time) ([]domain.HelperUsage, error) {
	rows, err := r.queries.ListStaleHelperReservations(ctx, before.UnixMilli())
	if err != nil {
		return nil, err
	}
	out := make([]domain.HelperUsage, len(rows))
	for i, row := range rows {
		out[i] = toDomainHelperUsage(row)
	}
	return out, nil
}

func toDomainHelperUsage(row *db.HelperUsage) domain.HelperUsage {
	u := domain.HelperUsage{
		ID:             uuidFromPg(row.ID),
		GameID:         uuidFromPg(row.GameID),
		RoundID:        uuidFromPg(row.RoundID),
		PlayerID:       uuidFromPg(row.PlayerID),
		Type:           domain.HelperType(row.HelperType),
		Cost:           int(row.Cost),
		Status:         domain.HelperStatus(row.Status),
		IdempotencyKey: row.IdempotencyKey,
		InputPrompt:    row.InputPrompt.String,
		TestID:         uuidFromPg(row.TestID),
		Content:        row.Content.String,
		ErrorCode:      row.ErrorCode.String,
		Provider:       row.Provider.String,
		Model:          row.Model.String,
		InputTokens:    intFromPg4(row.InputTokens),
		OutputTokens:   intFromPg4(row.OutputTokens),
		CreatedAt:      time.UnixMilli(row.CreatedAtMs).UTC(),
		DeliveredAt:    timeFromMs(row.DeliveredAtMs),
		RefundedAt:     timeFromMs(row.RefundedAtMs),
	}
	if row.CostUsd.Valid {
		u.CostUSD = &row.CostUsd.Float64
	}
	return u
}
