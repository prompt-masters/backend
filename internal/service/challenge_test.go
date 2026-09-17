package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// fakeChallengeRepository is an in-memory repository.ChallengeRepository that
// applies filters the way the postgres implementation does.
type fakeChallengeRepository struct {
	challenges []*domain.Challenge
	err        error

	listCalls  int
	lastFilter domain.ChallengeFilter
}

func (f *fakeChallengeRepository) List(_ context.Context, filter domain.ChallengeFilter) ([]*domain.Challenge, error) {
	f.listCalls++
	f.lastFilter = filter
	if f.err != nil {
		return nil, f.err
	}
	out := []*domain.Challenge{}
	for _, c := range f.challenges {
		if filter.Category != nil && c.Category != *filter.Category {
			continue
		}
		if filter.Difficulty != nil && c.Difficulty != *filter.Difficulty {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeChallengeRepository) GetByID(_ context.Context, id uuid.UUID) (*domain.Challenge, error) {
	if f.err != nil {
		return nil, f.err
	}
	for _, c := range f.challenges {
		if c.ID == id {
			return c, nil
		}
	}
	return nil, domain.ErrNotFound
}

func newChallenge(slug string, category domain.Category, difficulty domain.Difficulty) *domain.Challenge {
	return &domain.Challenge{ID: uuid.New(), Slug: slug, Category: category, Difficulty: difficulty}
}

func slugsOf(challenges []*domain.Challenge) []string {
	out := make([]string, len(challenges))
	for i, c := range challenges {
		out[i] = c.Slug
	}
	return out
}

func TestChallengeServiceList(t *testing.T) {
	repo := &fakeChallengeRepository{challenges: []*domain.Challenge{
		newChallenge("coding-easy", domain.CategoryCoding, domain.DifficultyEasy),
		newChallenge("coding-hard", domain.CategoryCoding, domain.DifficultyHard),
		newChallenge("reasoning-easy", domain.CategoryReasoning, domain.DifficultyEasy),
	}}
	svc := NewChallengeService(repo)

	tests := []struct {
		name       string
		in         ListChallengesInput
		wantSlugs  []string
		wantFields []string
	}{
		{name: "no filters", wantSlugs: []string{"coding-easy", "coding-hard", "reasoning-easy"}},
		{name: "category only", in: ListChallengesInput{Category: "coding"}, wantSlugs: []string{"coding-easy", "coding-hard"}},
		{name: "difficulty only", in: ListChallengesInput{Difficulty: "easy"}, wantSlugs: []string{"coding-easy", "reasoning-easy"}},
		{name: "combined", in: ListChallengesInput{Category: "coding", Difficulty: "hard"}, wantSlugs: []string{"coding-hard"}},
		{name: "values are trimmed and case-insensitive", in: ListChallengesInput{Category: " Coding ", Difficulty: "EASY"}, wantSlugs: []string{"coding-easy"}},
		{name: "blank values do not filter", in: ListChallengesInput{Category: "  ", Difficulty: ""}, wantSlugs: []string{"coding-easy", "coding-hard", "reasoning-easy"}},
		{name: "invalid category", in: ListChallengesInput{Category: "cooking"}, wantFields: []string{"category"}},
		{name: "invalid difficulty", in: ListChallengesInput{Difficulty: "impossible"}, wantFields: []string{"difficulty"}},
		{name: "both invalid", in: ListChallengesInput{Category: "cooking", Difficulty: "impossible"}, wantFields: []string{"category", "difficulty"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo.listCalls = 0
			got, err := svc.List(context.Background(), tt.in)

			if tt.wantFields != nil {
				var vErr *ValidationError
				if !errors.As(err, &vErr) {
					t.Fatalf("List() error = %v, want *ValidationError", err)
				}
				for _, field := range tt.wantFields {
					if !strings.HasPrefix(vErr.Fields[field], "must be one of: ") {
						t.Errorf("Fields[%q] = %q, want a list of allowed values", field, vErr.Fields[field])
					}
				}
				if len(vErr.Fields) != len(tt.wantFields) {
					t.Errorf("Fields = %v, want only %v", vErr.Fields, tt.wantFields)
				}
				if repo.listCalls != 0 {
					t.Error("repository was queried despite invalid input")
				}
				return
			}

			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if !reflect.DeepEqual(slugsOf(got), tt.wantSlugs) {
				t.Errorf("List() slugs = %v, want %v", slugsOf(got), tt.wantSlugs)
			}
		})
	}
}

func TestChallengeServiceListPropagatesRepositoryErrors(t *testing.T) {
	wantErr := errors.New("connection refused")
	svc := NewChallengeService(&fakeChallengeRepository{err: wantErr})

	_, err := svc.List(context.Background(), ListChallengesInput{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("List() error = %v, want %v", err, wantErr)
	}
}

func TestChallengeServiceGet(t *testing.T) {
	existing := newChallenge("coding-easy", domain.CategoryCoding, domain.DifficultyEasy)
	svc := NewChallengeService(&fakeChallengeRepository{challenges: []*domain.Challenge{existing}})

	tests := []struct {
		name    string
		id      uuid.UUID
		want    *domain.Challenge
		wantErr error
	}{
		{name: "existing challenge", id: existing.ID, want: existing},
		{name: "unknown ID", id: uuid.New(), wantErr: domain.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := svc.Get(context.Background(), tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Get() error = %v, want %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("Get() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestChallengeServiceCategories(t *testing.T) {
	svc := NewChallengeService(&fakeChallengeRepository{})

	if got := svc.Categories(); !reflect.DeepEqual(got, domain.Categories()) {
		t.Errorf("Categories() = %v, want %v", got, domain.Categories())
	}
}
