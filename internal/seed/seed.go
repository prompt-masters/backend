// Package seed loads development challenge data into the database.
package seed

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/prompt-masters/backend/internal/domain"
)

// MinChallengesPerCombination is how many challenges the seed provides for
// every category and difficulty pair, enough to play several rounds of a game
// locked to one pair without repeats.
const MinChallengesPerCombination = 3

type ChallengeUpserter interface {
	Upsert(ctx context.Context, c *domain.Challenge) (*domain.Challenge, error)
}

// Run validates every seed challenge and upserts them by slug, so running it
// more than once leaves the same data behind. Nothing is written when any
// challenge is invalid. It returns the number of challenges written.
func Run(ctx context.Context, repo ChallengeUpserter) (int, error) {
	all := Challenges()
	if err := Validate(all); err != nil {
		return 0, fmt.Errorf("invalid seed data: %w", err)
	}

	for i, c := range all {
		if _, err := repo.Upsert(ctx, c); err != nil {
			return i, fmt.Errorf("upserting challenge %q: %w", c.Slug, err)
		}
	}
	return len(all), nil
}

// Validate checks that the challenges are individually well formed and that
// together they cover every category and difficulty pair.
func Validate(challenges []*domain.Challenge) error {
	var errs []error
	slugs := make(map[string]bool, len(challenges))
	perCombination := make(map[string]int)

	for _, c := range challenges {
		if c.Slug == "" {
			errs = append(errs, errors.New("challenge with an empty slug"))
		} else if slugs[c.Slug] {
			errs = append(errs, fmt.Errorf("%s: duplicate slug", c.Slug))
		}
		slugs[c.Slug] = true

		if strings.TrimSpace(c.Title) == "" || strings.TrimSpace(c.Description) == "" {
			errs = append(errs, fmt.Errorf("%s: title and description are required", c.Slug))
		}
		if !c.Category.Valid() {
			errs = append(errs, fmt.Errorf("%s: invalid category %q", c.Slug, c.Category))
		}
		if !c.Difficulty.Valid() {
			errs = append(errs, fmt.Errorf("%s: invalid difficulty %q", c.Slug, c.Difficulty))
		}
		if c.Constraints.TimeLimitSeconds <= 0 || c.Constraints.MaxPromptChars <= 0 {
			errs = append(errs, fmt.Errorf("%s: time limit and max prompt chars must be positive", c.Slug))
		}
		if err := c.JudgeCriteria.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%s: judge criteria: %w", c.Slug, err))
		}
		perCombination[combination(c.Category, c.Difficulty)]++
	}

	for _, category := range domain.Categories() {
		for _, difficulty := range domain.Difficulties() {
			if n := perCombination[combination(category, difficulty)]; n < MinChallengesPerCombination {
				errs = append(errs, fmt.Errorf("%s/%s has %d challenges, want at least %d",
					category, difficulty, n, MinChallengesPerCombination))
			}
		}
	}

	return errors.Join(errs...)
}

func combination(c domain.Category, d domain.Difficulty) string {
	return string(c) + "/" + string(d)
}
