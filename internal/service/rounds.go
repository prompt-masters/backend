package service

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// RoundService serves a round's final submissions and results. Before a
// round's results are published, a player only ever sees their own
// submission; anything else is reported as not found, so its existence is
// not revealed either.
type RoundService struct {
	tx     repository.Transactor
	engine *GameEngine
}

func NewRoundService(tx repository.Transactor, engine *GameEngine) *RoundService {
	return &RoundService{tx: tx, engine: engine}
}

// Submit makes one of userID's completed tests their final submission for the
// round. It reports false for a repeat of their existing submission.
func (s *RoundService) Submit(ctx context.Context, userID uuid.UUID, roundRef, testRef string) (*domain.Submission, bool, error) {
	roundID, err := uuid.Parse(roundRef)
	if err != nil {
		return nil, false, domain.ErrNotFound
	}
	testID, err := uuid.Parse(testRef)
	if err != nil {
		return nil, false, domain.ErrInvalidTest
	}
	round, err := s.round(ctx, roundID)
	if err != nil {
		return nil, false, err
	}
	return s.engine.Submit(ctx, round.GameID, userID, roundID, testID)
}

// Get returns a submission to its owner at any time, and to the other players
// of its game once its round's results are published.
func (s *RoundService) Get(ctx context.Context, userID uuid.UUID, ref string) (*domain.Submission, error) {
	var sub *domain.Submission
	err := s.visible(ctx, userID, ref, func(repos repository.TxRepositories, found *domain.Submission, published bool) error {
		if found.PlayerID != userID && !published {
			return domain.ErrNotFound
		}
		sub = found
		return nil
	})
	return sub, err
}

// Score returns a submission's score, rank and rationale to the players of
// its game once its round's results are published. Before that its owner
// gets domain.ErrResultsNotReady and everyone else domain.ErrNotFound.
func (s *RoundService) Score(ctx context.Context, userID uuid.UUID, ref string) (*domain.RoundResult, error) {
	var result *domain.RoundResult
	err := s.visible(ctx, userID, ref, func(repos repository.TxRepositories, sub *domain.Submission, published bool) error {
		if !published {
			if sub.PlayerID == userID {
				return domain.ErrResultsNotReady
			}
			return domain.ErrNotFound
		}
		results, err := loadRoundResults(ctx, repos, sub.GameID, sub.RoundID)
		if err != nil {
			return err
		}
		for _, r := range results.Results {
			if r.SubmissionID == sub.ID {
				result = &r
				return nil
			}
		}
		return domain.ErrNotFound
	})
	return result, err
}

// RetryJudging judges a round again after judging it failed. Only the host
// may, and only while the round waits in Judging with a failed run.
func (s *RoundService) RetryJudging(ctx context.Context, userID uuid.UUID, roundRef string) error {
	roundID, err := uuid.Parse(roundRef)
	if err != nil {
		return domain.ErrNotFound
	}
	var gameID uuid.UUID
	err = s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		round, err := repos.Games.GetRound(ctx, roundID)
		if err != nil {
			return err
		}
		game, err := repos.Games.GetByID(ctx, round.GameID)
		if err != nil {
			return err
		}
		if !game.HasPlayer(userID) {
			return domain.ErrNotGamePlayer
		}
		if !game.IsHost(userID) {
			return domain.ErrNotGameHost
		}
		p := game.Progress
		if game.Status != domain.GameStatusInProgress || p.RoundID != roundID || p.Phase != domain.GamePhaseJudging {
			return domain.ErrJudgingNotFailed
		}
		run, err := repos.Judging.Get(ctx, roundID)
		if errors.Is(err, domain.ErrNotFound) || (err == nil && run.Status != domain.JudgingFailed) {
			return domain.ErrJudgingNotFailed
		}
		gameID = game.ID
		return err
	})
	if err != nil {
		return err
	}
	s.engine.Rejudge(gameID, roundID)
	return nil
}

// visible loads a submission for userID, hiding it from anyone outside its
// game, and hands it to fn with whether its round's results are published.
func (s *RoundService) visible(ctx context.Context, userID uuid.UUID, ref string, fn func(repository.TxRepositories, *domain.Submission, bool) error) error {
	id, err := uuid.Parse(ref)
	if err != nil {
		return domain.ErrNotFound
	}
	return s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		sub, err := repos.Submissions.Get(ctx, id)
		if err != nil {
			return err
		}
		game, err := repos.Games.GetByID(ctx, sub.GameID)
		if err != nil {
			return err
		}
		if !game.HasPlayer(userID) {
			return domain.ErrNotFound
		}
		published, err := resultsPublished(ctx, repos, game, sub.RoundID)
		if err != nil {
			return err
		}
		return fn(repos, sub, published)
	})
}

func (s *RoundService) round(ctx context.Context, roundID uuid.UUID) (*domain.Round, error) {
	var round *domain.Round
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		round, err = repos.Games.GetRound(ctx, roundID)
		return err
	})
	return round, err
}
