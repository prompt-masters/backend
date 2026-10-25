package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// loadRoundResults assembles a round's published results from its scores,
// submissions and players, best rank first. It is the one place results are
// built, so the broadcast, reconnect snapshots and the score endpoint agree.
func loadRoundResults(ctx context.Context, repos repository.TxRepositories, gameID, roundID uuid.UUID) (domain.RoundResults, error) {
	game, err := repos.Games.GetByID(ctx, gameID)
	if err != nil {
		return domain.RoundResults{}, err
	}
	round, err := repos.Games.GetRound(ctx, roundID)
	if err != nil {
		return domain.RoundResults{}, err
	}
	scores, err := repos.Judging.ListScores(ctx, roundID)
	if err != nil {
		return domain.RoundResults{}, err
	}
	subs, err := repos.Submissions.ListByRound(ctx, roundID)
	if err != nil {
		return domain.RoundResults{}, err
	}
	byID := make(map[uuid.UUID]domain.Submission, len(subs))
	for _, s := range subs {
		byID[s.ID] = s
	}
	names := make(map[uuid.UUID]string, len(game.Players))
	for _, p := range game.Players {
		names[p.UserID] = p.Username
	}

	results := domain.RoundResults{RoundID: roundID, Number: round.Number, Results: make([]domain.RoundResult, len(scores))}
	for i, s := range scores {
		r := domain.RoundResult{Score: s, DisplayName: names[s.PlayerID]}
		if sub, ok := byID[s.SubmissionID]; ok && !s.NoSubmission {
			r.Prompt, r.Output, r.PromptLength, r.TestCount, r.Source = sub.Prompt, sub.Output, sub.PromptLength, sub.TestCount, sub.Source
		}
		results.Results[i] = r
	}
	return results, nil
}

// resultsPublished reports whether roundID's results are out: its scores are
// committed and the game has moved past judging it.
func resultsPublished(ctx context.Context, repos repository.TxRepositories, game *domain.Game, roundID uuid.UUID) (bool, error) {
	run, err := repos.Judging.Get(ctx, roundID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if run.Status != domain.JudgingCompleted {
		return false, nil
	}
	p := game.Progress
	if p.RoundID == roundID && (p.Phase == domain.GamePhaseRoundActive || p.Phase == domain.GamePhaseJudging) {
		return false, nil
	}
	return true, nil
}
