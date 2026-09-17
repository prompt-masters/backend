package service

import (
	"context"
	"fmt"
	"math/rand/v2"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// Random is the randomness source ChallengeFactory selects with. IntN returns
// a value in [0, n). *rand.Rand satisfies it, so tests can inject a seeded
// generator or a stub.
type Random interface {
	IntN(n int) int
}

// MathRandom draws from math/rand/v2's top-level generator, which is
// automatically seeded and safe for concurrent use.
type MathRandom struct{}

func (MathRandom) IntN(n int) int {
	return rand.IntN(n)
}

// ChallengeRequest describes the game asking for its next challenge. A blank
// Category or Difficulty matches any value.
type ChallengeRequest struct {
	Category   domain.Category
	Difficulty domain.Difficulty
	// UsedChallengeIDs are the challenges already played in this game.
	UsedChallengeIDs []uuid.UUID
}

// ChallengeFactory picks the next challenge for a game.
type ChallengeFactory struct {
	challenges repository.ChallengeRepository
	random     Random
}

func NewChallengeFactory(challenges repository.ChallengeRepository, random Random) *ChallengeFactory {
	return &ChallengeFactory{challenges: challenges, random: random}
}

// Create randomly selects a challenge matching the request's category and
// difficulty. Challenges already used in the game are skipped while unused
// eligible ones remain; once every eligible challenge has been used, any of
// them may repeat. The returned challenge carries its constraints and judge
// criteria.
func (f *ChallengeFactory) Create(ctx context.Context, req ChallengeRequest) (*domain.Challenge, error) {
	filter, err := parseChallengeFilter(string(req.Category), string(req.Difficulty))
	if err != nil {
		return nil, err
	}

	eligible, err := f.challenges.List(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("listing eligible challenges: %w", err)
	}
	if len(eligible) == 0 {
		return nil, domain.ErrNoEligibleChallenge
	}

	candidates := excludeUsed(eligible, req.UsedChallengeIDs)
	if len(candidates) == 0 {
		candidates = eligible
	}

	return candidates[f.random.IntN(len(candidates))], nil
}

func excludeUsed(challenges []*domain.Challenge, usedIDs []uuid.UUID) []*domain.Challenge {
	if len(usedIDs) == 0 {
		return challenges
	}
	used := make(map[uuid.UUID]struct{}, len(usedIDs))
	for _, id := range usedIDs {
		used[id] = struct{}{}
	}

	unused := make([]*domain.Challenge, 0, len(challenges))
	for _, c := range challenges {
		if _, ok := used[c.ID]; !ok {
			unused = append(unused, c)
		}
	}
	return unused
}
