package service

import (
	"context"
	"errors"
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// stubRandom always picks index (wrapped into range) and records the n it
// was asked for.
type stubRandom struct {
	index int
	gotN  []int
}

func (s *stubRandom) IntN(n int) int {
	s.gotN = append(s.gotN, n)
	return s.index % n
}

func factoryFixtures() []*domain.Challenge {
	return []*domain.Challenge{
		newChallenge("coding-easy-1", domain.CategoryCoding, domain.DifficultyEasy),
		newChallenge("coding-easy-2", domain.CategoryCoding, domain.DifficultyEasy),
		newChallenge("coding-easy-3", domain.CategoryCoding, domain.DifficultyEasy),
		newChallenge("coding-hard-1", domain.CategoryCoding, domain.DifficultyHard),
		newChallenge("reasoning-easy-1", domain.CategoryReasoning, domain.DifficultyEasy),
		newChallenge("reasoning-medium-1", domain.CategoryReasoning, domain.DifficultyMedium),
	}
}

func idsOf(challenges []*domain.Challenge, slugs ...string) []uuid.UUID {
	ids := []uuid.UUID{}
	for _, c := range challenges {
		for _, s := range slugs {
			if c.Slug == s {
				ids = append(ids, c.ID)
			}
		}
	}
	return ids
}

func TestChallengeFactoryOnlySelectsMatchingChallenges(t *testing.T) {
	tests := []struct {
		name      string
		req       ChallengeRequest
		wantSlugs []string
	}{
		{
			name:      "category and difficulty",
			req:       ChallengeRequest{Category: domain.CategoryCoding, Difficulty: domain.DifficultyEasy},
			wantSlugs: []string{"coding-easy-1", "coding-easy-2", "coding-easy-3"},
		},
		{
			name:      "category only",
			req:       ChallengeRequest{Category: domain.CategoryReasoning},
			wantSlugs: []string{"reasoning-easy-1", "reasoning-medium-1"},
		},
		{
			name:      "difficulty only",
			req:       ChallengeRequest{Difficulty: domain.DifficultyEasy},
			wantSlugs: []string{"coding-easy-1", "coding-easy-2", "coding-easy-3", "reasoning-easy-1"},
		},
		{
			name:      "no configuration matches everything",
			req:       ChallengeRequest{},
			wantSlugs: slugsOf(factoryFixtures()),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeChallengeRepository{challenges: factoryFixtures()}

			// Drive the random source through every index so each eligible
			// challenge is selected once.
			var picked []string
			for i := range len(tt.wantSlugs) {
				rng := &stubRandom{index: i}
				got, err := NewChallengeFactory(repo, rng).Create(context.Background(), tt.req)
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				if rng.gotN[0] != len(tt.wantSlugs) {
					t.Errorf("IntN called with n = %d, want %d eligible challenges", rng.gotN[0], len(tt.wantSlugs))
				}
				picked = append(picked, got.Slug)
			}

			if !reflect.DeepEqual(picked, tt.wantSlugs) {
				t.Errorf("selectable challenges = %v, want %v", picked, tt.wantSlugs)
			}
		})
	}
}

func TestChallengeFactoryAvoidsChallengesUsedInTheGame(t *testing.T) {
	fixtures := factoryFixtures()
	req := ChallengeRequest{Category: domain.CategoryCoding, Difficulty: domain.DifficultyEasy}

	tests := []struct {
		name      string
		used      []string
		wantSlugs []string
	}{
		{name: "nothing used", wantSlugs: []string{"coding-easy-1", "coding-easy-2", "coding-easy-3"}},
		{name: "one used", used: []string{"coding-easy-2"}, wantSlugs: []string{"coding-easy-1", "coding-easy-3"}},
		{name: "all but one used", used: []string{"coding-easy-1", "coding-easy-3"}, wantSlugs: []string{"coding-easy-2"}},
		{
			name:      "used challenges outside the configuration are ignored",
			used:      []string{"coding-hard-1", "reasoning-easy-1"},
			wantSlugs: []string{"coding-easy-1", "coding-easy-2", "coding-easy-3"},
		},
		{
			name:      "repeats only once every eligible challenge is used",
			used:      []string{"coding-easy-1", "coding-easy-2", "coding-easy-3"},
			wantSlugs: []string{"coding-easy-1", "coding-easy-2", "coding-easy-3"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &fakeChallengeRepository{challenges: fixtures}
			req := req
			req.UsedChallengeIDs = idsOf(fixtures, tt.used...)

			var picked []string
			for i := range len(tt.wantSlugs) {
				got, err := NewChallengeFactory(repo, &stubRandom{index: i}).Create(context.Background(), req)
				if err != nil {
					t.Fatalf("Create() error = %v", err)
				}
				picked = append(picked, got.Slug)
			}

			if !reflect.DeepEqual(picked, tt.wantSlugs) {
				t.Errorf("selectable challenges = %v, want %v", picked, tt.wantSlugs)
			}
		})
	}
}

func TestChallengeFactoryPlaysAFullGameWithoutRepeats(t *testing.T) {
	repo := &fakeChallengeRepository{challenges: factoryFixtures()}
	factory := NewChallengeFactory(repo, rand.New(rand.NewPCG(7, 42)))
	req := ChallengeRequest{Difficulty: domain.DifficultyEasy}
	const eligible = 4

	seen := map[uuid.UUID]bool{}
	for round := range eligible {
		got, err := factory.Create(context.Background(), req)
		if err != nil {
			t.Fatalf("round %d: Create() error = %v", round, err)
		}
		if got.Difficulty != domain.DifficultyEasy {
			t.Fatalf("round %d: got %s challenge, want easy", round, got.Difficulty)
		}
		if seen[got.ID] {
			t.Fatalf("round %d: challenge %s repeated while unused challenges remained", round, got.Slug)
		}
		seen[got.ID] = true
		req.UsedChallengeIDs = append(req.UsedChallengeIDs, got.ID)
	}

	// Every eligible challenge is used now, so the factory must still answer.
	got, err := factory.Create(context.Background(), req)
	if err != nil {
		t.Fatalf("after exhausting the pool: Create() error = %v", err)
	}
	if !seen[got.ID] {
		t.Errorf("after exhausting the pool: got %s, want one of the used easy challenges", got.Slug)
	}
}

func TestChallengeFactoryReturnsConstraintsAndJudgeCriteria(t *testing.T) {
	want := newChallenge("summary", domain.CategorySummarization, domain.DifficultyMedium)
	want.Constraints = domain.ChallengeConstraints{TimeLimitSeconds: 90, MaxPromptChars: 200, ForbiddenWords: []string{"tl;dr"}}
	want.JudgeCriteria = domain.JudgeCriteria{
		PassingScore: 65,
		Criteria: []domain.JudgeCriterion{
			{Name: "faithfulness", Description: "No invented facts.", Weight: 1, MaxScore: 10},
		},
	}
	repo := &fakeChallengeRepository{challenges: []*domain.Challenge{want}}

	got, err := NewChallengeFactory(repo, &stubRandom{}).Create(context.Background(), ChallengeRequest{Category: domain.CategorySummarization})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !reflect.DeepEqual(got.Constraints, want.Constraints) {
		t.Errorf("Constraints = %+v, want %+v", got.Constraints, want.Constraints)
	}
	if !reflect.DeepEqual(got.JudgeCriteria, want.JudgeCriteria) {
		t.Errorf("JudgeCriteria = %+v, want %+v", got.JudgeCriteria, want.JudgeCriteria)
	}
}

func TestChallengeFactoryErrors(t *testing.T) {
	repoErr := errors.New("connection refused")

	tests := []struct {
		name         string
		repo         *fakeChallengeRepository
		req          ChallengeRequest
		wantErr      error
		wantFields   []string
		wantNoLookup bool
	}{
		{
			name:    "no eligible challenge",
			repo:    &fakeChallengeRepository{challenges: factoryFixtures()},
			req:     ChallengeRequest{Category: domain.CategoryCreativeWriting},
			wantErr: domain.ErrNoEligibleChallenge,
		},
		{
			name:    "empty challenge table",
			repo:    &fakeChallengeRepository{},
			wantErr: domain.ErrNoEligibleChallenge,
		},
		{
			name:    "repository failure",
			repo:    &fakeChallengeRepository{err: repoErr},
			wantErr: repoErr,
		},
		{
			name:         "invalid configuration",
			repo:         &fakeChallengeRepository{challenges: factoryFixtures()},
			req:          ChallengeRequest{Category: "cooking", Difficulty: "nightmare"},
			wantFields:   []string{"category", "difficulty"},
			wantNoLookup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rng := &stubRandom{}
			got, err := NewChallengeFactory(tt.repo, rng).Create(context.Background(), tt.req)

			if got != nil {
				t.Errorf("Create() challenge = %v, want nil", got)
			}
			if tt.wantFields != nil {
				var vErr *ValidationError
				if !errors.As(err, &vErr) {
					t.Fatalf("Create() error = %v, want *ValidationError", err)
				}
				for _, f := range tt.wantFields {
					if vErr.Fields[f] == "" {
						t.Errorf("Fields[%q] is empty; got %v", f, vErr.Fields)
					}
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Create() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantNoLookup && tt.repo.listCalls != 0 {
				t.Error("repository was queried despite an invalid configuration")
			}
			if len(rng.gotN) != 0 {
				t.Errorf("random source was used (%v) although nothing could be selected", rng.gotN)
			}
		})
	}
}
