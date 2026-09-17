package seed

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/prompt-masters/backend/internal/domain"
)

type recordingUpserter struct {
	slugs  []string
	failOn string
}

func (r *recordingUpserter) Upsert(_ context.Context, c *domain.Challenge) (*domain.Challenge, error) {
	if c.Slug == r.failOn {
		return nil, errors.New("boom")
	}
	r.slugs = append(r.slugs, c.Slug)
	return c, nil
}

func TestSeedDataIsValid(t *testing.T) {
	if err := Validate(Challenges()); err != nil {
		t.Fatalf("seed data is invalid:\n%v", err)
	}
}

func TestSeedDataCoversEveryCategoryAndDifficulty(t *testing.T) {
	counts := map[domain.Category]map[domain.Difficulty]int{}
	for _, c := range Challenges() {
		if counts[c.Category] == nil {
			counts[c.Category] = map[domain.Difficulty]int{}
		}
		counts[c.Category][c.Difficulty]++
	}

	for _, category := range domain.Categories() {
		for _, difficulty := range domain.Difficulties() {
			if n := counts[category][difficulty]; n < MinChallengesPerCombination {
				t.Errorf("%s/%s has %d challenges, want at least %d", category, difficulty, n, MinChallengesPerCombination)
			}
		}
	}
}

func TestValidateReportsProblems(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func([]*domain.Challenge) []*domain.Challenge
		wantErr string
	}{
		{
			name:    "duplicate slug",
			mutate:  func(cs []*domain.Challenge) []*domain.Challenge { cs[1].Slug = cs[0].Slug; return cs },
			wantErr: "duplicate slug",
		},
		{
			name: "invalid rubric",
			mutate: func(cs []*domain.Challenge) []*domain.Challenge {
				cs[0].JudgeCriteria.Criteria[0].Weight = 5
				return cs
			},
			wantErr: "sum to 1",
		},
		{
			name:    "invalid category",
			mutate:  func(cs []*domain.Challenge) []*domain.Challenge { cs[0].Category = "cooking"; return cs },
			wantErr: "invalid category",
		},
		{
			name:    "missing coverage",
			mutate:  func(cs []*domain.Challenge) []*domain.Challenge { return cs[1:] },
			wantErr: "want at least",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.mutate(Challenges()))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestRunUpsertsEveryChallenge(t *testing.T) {
	repo := &recordingUpserter{}

	n, err := Run(context.Background(), repo)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if want := len(Challenges()); n != want || len(repo.slugs) != want {
		t.Errorf("Run() wrote %d (reported %d), want %d", len(repo.slugs), n, want)
	}
}

func TestRunStopsOnUpsertFailure(t *testing.T) {
	failOn := Challenges()[2].Slug
	repo := &recordingUpserter{failOn: failOn}

	n, err := Run(context.Background(), repo)
	if err == nil || !strings.Contains(err.Error(), failOn) {
		t.Fatalf("Run() error = %v, want it to name %q", err, failOn)
	}
	if n != 2 {
		t.Errorf("Run() reported %d written, want 2", n)
	}
}
