package service

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

type ListChallengesInput struct {
	Category   string
	Difficulty string
}

type ChallengeService struct {
	challenges repository.ChallengeRepository
}

func NewChallengeService(challenges repository.ChallengeRepository) *ChallengeService {
	return &ChallengeService{challenges: challenges}
}

func (s *ChallengeService) List(ctx context.Context, in ListChallengesInput) ([]*domain.Challenge, error) {
	filter, err := parseChallengeFilter(in.Category, in.Difficulty)
	if err != nil {
		return nil, err
	}
	return s.challenges.List(ctx, filter)
}

func (s *ChallengeService) Get(ctx context.Context, id uuid.UUID) (*domain.Challenge, error) {
	return s.challenges.GetByID(ctx, id)
}

func (s *ChallengeService) Categories() []domain.Category {
	return domain.Categories()
}

// parseChallengeFilter normalizes and validates optional category and
// difficulty values. Blank values do not filter.
func parseChallengeFilter(category, difficulty string) (domain.ChallengeFilter, error) {
	var filter domain.ChallengeFilter
	fields := make(map[string]string)

	if v := strings.ToLower(strings.TrimSpace(category)); v != "" {
		c := domain.Category(v)
		if c.Valid() {
			filter.Category = &c
		} else {
			fields["category"] = "must be one of: " + joinValues(domain.Categories())
		}
	}
	if v := strings.ToLower(strings.TrimSpace(difficulty)); v != "" {
		d := domain.Difficulty(v)
		if d.Valid() {
			filter.Difficulty = &d
		} else {
			fields["difficulty"] = "must be one of: " + joinValues(domain.Difficulties())
		}
	}

	if len(fields) > 0 {
		return domain.ChallengeFilter{}, &ValidationError{Fields: fields}
	}
	return filter, nil
}

func joinValues[T ~string](values []T) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = string(v)
	}
	return strings.Join(parts, ", ")
}
