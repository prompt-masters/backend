package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// JudgingRepository stores judging runs and the scores they produce.
type JudgingRepository interface {
	// Claim takes the round's judging run for the caller: a new run, a failed
	// one, or one still running since before staleBefore (its worker must
	// have stopped). It reports the attempt number it claimed, or false when
	// the run is held or already completed.
	Claim(ctx context.Context, roundID, gameID uuid.UUID, now, staleBefore time.Time) (int, bool, error)
	// Get returns domain.ErrNotFound when the round has no run.
	Get(ctx context.Context, roundID uuid.UUID) (*domain.JudgingRun, error)
	// Complete and Fail finish the claimed attempt. They return
	// domain.ErrStaleGameState when the attempt is no longer the claimant.
	Complete(ctx context.Context, roundID uuid.UUID, attempt int, at time.Time, call domain.JudgeCall) error
	Fail(ctx context.Context, roundID uuid.UUID, attempt int, at time.Time, errorCode string) error
	// CreateScore fails on a second score for the same player and round.
	CreateScore(ctx context.Context, s domain.Score) error
	// ListScores returns the round's scores, best rank first.
	ListScores(ctx context.Context, roundID uuid.UUID) ([]domain.Score, error)
}
