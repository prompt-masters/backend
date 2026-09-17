package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

type ChallengeRepository struct {
	queries *db.Queries
}

func NewChallengeRepository(queries *db.Queries) *ChallengeRepository {
	return &ChallengeRepository{queries: queries}
}

func (r *ChallengeRepository) List(ctx context.Context, filter domain.ChallengeFilter) ([]*domain.Challenge, error) {
	var params db.ListChallengesParams
	if filter.Category != nil {
		params.Category = pgtype.Text{String: string(*filter.Category), Valid: true}
	}
	if filter.Difficulty != nil {
		params.Difficulty = pgtype.Text{String: string(*filter.Difficulty), Valid: true}
	}

	rows, err := r.queries.ListChallenges(ctx, params)
	if err != nil {
		return nil, err
	}

	challenges := make([]*domain.Challenge, 0, len(rows))
	for _, row := range rows {
		c, err := toDomainChallenge(row)
		if err != nil {
			return nil, err
		}
		challenges = append(challenges, c)
	}
	return challenges, nil
}

func (r *ChallengeRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Challenge, error) {
	row, err := r.queries.GetChallengeByID(ctx, pgUUID(id))
	if err != nil {
		return nil, translateGetError(err)
	}
	return toDomainChallenge(row)
}

// Upsert inserts the challenge or, when one with the same slug exists,
// overwrites it. It backs the idempotent dev seed.
func (r *ChallengeRepository) Upsert(ctx context.Context, c *domain.Challenge) (*domain.Challenge, error) {
	constraints, err := json.Marshal(c.Constraints)
	if err != nil {
		return nil, fmt.Errorf("encoding constraints for challenge %q: %w", c.Slug, err)
	}
	judgeCriteria, err := json.Marshal(c.JudgeCriteria)
	if err != nil {
		return nil, fmt.Errorf("encoding judge criteria for challenge %q: %w", c.Slug, err)
	}

	row, err := r.queries.UpsertChallenge(ctx, db.UpsertChallengeParams{
		Slug:          c.Slug,
		Title:         c.Title,
		Description:   c.Description,
		Category:      string(c.Category),
		Difficulty:    string(c.Difficulty),
		Constraints:   constraints,
		JudgeCriteria: judgeCriteria,
	})
	if err != nil {
		return nil, err
	}
	return toDomainChallenge(row)
}

func toDomainChallenge(c *db.Challenge) (*domain.Challenge, error) {
	challenge := &domain.Challenge{
		ID:          uuidFromPg(c.ID),
		Slug:        c.Slug,
		Title:       c.Title,
		Description: c.Description,
		Category:    domain.Category(c.Category),
		Difficulty:  domain.Difficulty(c.Difficulty),
		CreatedAt:   c.CreatedAt.Time,
		UpdatedAt:   c.UpdatedAt.Time,
	}
	if err := json.Unmarshal(c.Constraints, &challenge.Constraints); err != nil {
		return nil, fmt.Errorf("decoding constraints for challenge %s: %w", challenge.ID, err)
	}
	if err := json.Unmarshal(c.JudgeCriteria, &challenge.JudgeCriteria); err != nil {
		return nil, fmt.Errorf("decoding judge criteria for challenge %s: %w", challenge.ID, err)
	}
	return challenge, nil
}
