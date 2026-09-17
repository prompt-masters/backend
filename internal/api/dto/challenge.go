package dto

import (
	"time"

	"github.com/prompt-masters/backend/internal/domain"
)

type ChallengeResponse struct {
	ID            string                      `json:"id"`
	Slug          string                      `json:"slug"`
	Title         string                      `json:"title"`
	Description   string                      `json:"description"`
	Category      domain.Category             `json:"category"`
	Difficulty    domain.Difficulty           `json:"difficulty"`
	Constraints   domain.ChallengeConstraints `json:"constraints"`
	JudgeCriteria domain.JudgeCriteria        `json:"judge_criteria"`
	CreatedAt     time.Time                   `json:"created_at"`
	UpdatedAt     time.Time                   `json:"updated_at"`
}

func ChallengeFromDomain(c *domain.Challenge) ChallengeResponse {
	return ChallengeResponse{
		ID:            c.ID.String(),
		Slug:          c.Slug,
		Title:         c.Title,
		Description:   c.Description,
		Category:      c.Category,
		Difficulty:    c.Difficulty,
		Constraints:   c.Constraints,
		JudgeCriteria: c.JudgeCriteria,
		CreatedAt:     c.CreatedAt,
		UpdatedAt:     c.UpdatedAt,
	}
}

func ChallengesFromDomain(challenges []*domain.Challenge) []ChallengeResponse {
	out := make([]ChallengeResponse, len(challenges))
	for i, c := range challenges {
		out[i] = ChallengeFromDomain(c)
	}
	return out
}
