package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// Score components the judge rates; efficiency is computed, never judged.
const (
	criterionAccuracy   = "accuracy"
	criterionSimplicity = "simplicity"
	criterionCreativity = "creativity"
	criterionAdherence  = "adherence"
)

// Judging error codes, besides the upper-cased ai.ErrorKind of an AI failure.
const (
	JudgingErrorNoChallenge = "NO_CHALLENGE"
	JudgingErrorCanceled    = "CANCELED"
)

// errClaimLost means another worker took the run over; this one stops quietly.
var errClaimLost = errors.New("judging run was claimed by another worker")

// AIJudge is the part of the AI layer judging needs; *ai.Orchestrator
// satisfies it.
type AIJudge interface {
	Judge(ctx context.Context, in ai.JudgeInput) (ai.JudgeOutput, error)
}

// RoundJudge judges a round once it enters Judging. It is the engine's
// RoundJudger, so it always runs on its own goroutine, with its own context,
// and never under the game's lock.
//
// Judging is claimed through the round's judging_runs row: a worker only
// judges after claiming the run, and it writes every score in the same
// transaction that marks its own attempt completed. A second worker, another
// instance or a retry therefore finds the run held or done and does nothing,
// and a round never gets two sets of scores or partial ones.
//
// The judge sees only the challenge and, per anonymous label (A, B, C, ...
// shuffled afresh each round), a prompt and its output. Player identity stays
// on the server.
type RoundJudge struct {
	tx     repository.Transactor
	ai     AIJudge
	engine *GameEngine
	events GameEventPublisher
	clock  Clock
	random Random
	logger *log.Logger
	cfg    config.JudgingConfig
	sleep  func(context.Context, time.Duration) error
}

func NewRoundJudge(judge AIJudge, tx repository.Transactor, engine *GameEngine, events GameEventPublisher, clock Clock, random Random, logger *log.Logger, cfg config.JudgingConfig) *RoundJudge {
	return &RoundJudge{tx: tx, ai: judge, engine: engine, events: events, clock: clock, random: random, logger: logger, cfg: cfg, sleep: sleepCtx}
}

var _ RoundJudger = (*RoundJudge)(nil)

type claimState int

const (
	claimHeld claimState = iota
	claimTaken
	claimDone
)

// JudgeRound judges roundID, retrying a failed run up to cfg.MaxRuns times
// with backoff. When every run fails the round stays in Judging and the room
// is told; the host can retry (see RoundService.RetryJudging).
func (j *RoundJudge) JudgeRound(ctx context.Context, gameID, roundID uuid.UUID) {
	for run := 1; ; run++ {
		state, attempt, err := j.claim(ctx, gameID, roundID)
		if err != nil {
			j.logger.Printf("judging op=claim game_id=%s round_id=%s error=%q", gameID, roundID, err)
			return
		}
		switch state {
		case claimHeld:
			return
		case claimDone:
			// Scores were committed but the round never moved on (the server
			// stopped in between): finish publishing them.
			j.publish(ctx, gameID, roundID)
			return
		}

		started := time.Now()
		err = j.judge(ctx, gameID, roundID, attempt)
		if err == nil {
			j.logger.Printf("judging op=completed game_id=%s round_id=%s attempt=%d latency_ms=%d", gameID, roundID, attempt, time.Since(started).Milliseconds())
			j.publish(ctx, gameID, roundID)
			return
		}
		if errors.Is(err, errClaimLost) {
			return
		}

		code, retryable := judgingError(err)
		if ctx.Err() != nil {
			code = JudgingErrorCanceled // shutting down: a restart judges it again
		}
		j.fail(roundID, attempt, code)
		j.logger.Printf("judging op=failed game_id=%s round_id=%s attempt=%d run=%d error_code=%s error=%q", gameID, roundID, attempt, run, code, err)
		if ctx.Err() != nil {
			return
		}
		if !retryable || run >= j.cfg.MaxRuns {
			j.events.JudgingFailed(ctx, gameID, roundID, retryable)
			return
		}
		if err := j.sleep(ctx, j.cfg.RetryDelay<<(run-1)); err != nil {
			return
		}
	}
}

// claim takes the round's judging run if the round is still waiting in
// Judging and nobody holds it.
func (j *RoundJudge) claim(ctx context.Context, gameID, roundID uuid.UUID) (claimState, int, error) {
	state, attempt := claimHeld, 0
	err := j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		game, err := repos.Games.LockByID(ctx, gameID)
		if err != nil {
			return err
		}
		p := game.Progress
		if game.Status != domain.GameStatusInProgress || p.Phase != domain.GamePhaseJudging || p.RoundID != roundID {
			return nil
		}
		run, err := repos.Judging.Get(ctx, roundID)
		switch {
		case err == nil && run.Status == domain.JudgingCompleted:
			state = claimDone
			return nil
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return err
		}
		now := j.clock.Now()
		var ok bool
		if attempt, ok, err = repos.Judging.Claim(ctx, roundID, gameID, now, now.Add(-j.cfg.StaleAfter)); err != nil {
			return err
		}
		if ok {
			state = claimTaken
		}
		return nil
	})
	return state, attempt, err
}

// judge scores the round for the claimed attempt and commits the scores
// together with completing the run.
func (j *RoundJudge) judge(ctx context.Context, gameID, roundID uuid.UUID, attempt int) error {
	var (
		game      *domain.Game
		challenge *domain.Challenge
		subs      []domain.Submission
	)
	err := j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		if game, err = repos.Games.GetByID(ctx, gameID); err != nil {
			return err
		}
		round, err := repos.Games.GetRound(ctx, roundID)
		if err != nil {
			return err
		}
		if round.ChallengeID == uuid.Nil {
			return errNoChallenge
		}
		if challenge, err = repos.Challenges.GetByID(ctx, round.ChallengeID); err != nil {
			return err
		}
		subs, err = repos.Submissions.ListByRound(ctx, roundID)
		return err
	})
	if err != nil {
		return err
	}

	labeled := j.shuffle(subs)
	var out ai.JudgeOutput
	if len(labeled) > 0 {
		in := JudgeRequest(challenge, labeled)
		in.Metadata = map[string]string{"game_id": gameID.String(), "round_id": roundID.String()}
		if out, err = j.ai.Judge(ctx, in); err != nil {
			return err
		}
	}

	scores := ScoreRound(game, roundID, labeled, out, j.cfg.Weights, j.clock.Now())
	call := domain.JudgeCall{Provider: out.Provider, Model: out.Model, InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, CostUSD: out.Usage.CostUSD}
	return j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		err := repos.Judging.Complete(ctx, roundID, attempt, j.clock.Now(), call)
		if errors.Is(err, domain.ErrStaleGameState) {
			return errClaimLost
		}
		if err != nil {
			return err
		}
		for _, s := range scores {
			if err := repos.Judging.CreateScore(ctx, s); err != nil {
				return err
			}
		}
		return nil
	})
}

// publish moves the round to Results, then sends its results to the room:
// the first moment anyone sees another player's prompt and output.
func (j *RoundJudge) publish(ctx context.Context, gameID, roundID uuid.UUID) {
	if err := j.engine.CompleteJudging(ctx, gameID, roundID); err != nil {
		j.logger.Printf("judging op=complete game_id=%s round_id=%s error=%q", gameID, roundID, err)
		return
	}
	var results domain.RoundResults
	err := j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		results, err = loadRoundResults(ctx, repos, gameID, roundID)
		return err
	})
	if err != nil {
		j.logger.Printf("judging op=load_results game_id=%s round_id=%s error=%q", gameID, roundID, err)
		return
	}
	j.events.RoundResults(ctx, gameID, results)
}

// fail records a failed attempt on its own context, since ctx may be done.
func (j *RoundJudge) fail(roundID uuid.UUID, attempt int, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	err := j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		return repos.Judging.Fail(ctx, roundID, attempt, j.clock.Now(), code)
	})
	if err != nil && !errors.Is(err, domain.ErrStaleGameState) {
		j.logger.Printf("judging op=mark_failed round_id=%s error=%q", roundID, err)
	}
}

// LabeledSubmission is a submission under its anonymous label.
type LabeledSubmission struct {
	Label string
	domain.Submission
}

// shuffle labels the submissions A, B, C, ... in a fresh random order, so
// neither labels nor their order reveal who submitted what, or when.
func (j *RoundJudge) shuffle(subs []domain.Submission) []LabeledSubmission {
	shuffled := make([]domain.Submission, len(subs))
	copy(shuffled, subs)
	for i := len(shuffled) - 1; i > 0; i-- {
		k := j.random.IntN(i + 1)
		shuffled[i], shuffled[k] = shuffled[k], shuffled[i]
	}
	labeled := make([]LabeledSubmission, len(shuffled))
	for i, s := range shuffled {
		labeled[i] = LabeledSubmission{Label: label(i), Submission: s}
	}
	return labeled
}

// label names the i-th candidate: A..Z, then AA, AB, ...
func label(i int) string {
	name := ""
	for i++; i > 0; i = (i - 1) / 26 {
		name = string(rune('A'+(i-1)%26)) + name
	}
	return name
}

var errNoChallenge = errors.New("round has no challenge")

// JudgeRequest is everything the judge is told: the challenge, the rubric,
// and each label's prompt and output. It carries no player, submission or
// timing detail.
func JudgeRequest(c *domain.Challenge, labeled []LabeledSubmission) ai.JudgeInput {
	var rubric strings.Builder
	for _, cr := range c.JudgeCriteria.Criteria {
		fmt.Fprintf(&rubric, " %s (%.0f%%): %s", cr.Name, cr.Weight*100, cr.Description)
	}
	var constraints []string
	if len(c.Constraints.RequiredElements) > 0 {
		constraints = append(constraints, "the output must include "+strings.Join(c.Constraints.RequiredElements, ", "))
	}
	if c.Constraints.OutputFormat != "" {
		constraints = append(constraints, "the output must be "+c.Constraints.OutputFormat)
	}
	if len(c.Constraints.ForbiddenWords) > 0 {
		constraints = append(constraints, "the prompt must not use "+strings.Join(c.Constraints.ForbiddenWords, ", "))
	}
	if c.Constraints.MaxPromptChars > 0 {
		constraints = append(constraints, fmt.Sprintf("the prompt must be at most %d characters", c.Constraints.MaxPromptChars))
	}
	adherence := "How well the prompt and output respect the task's constraints."
	if len(constraints) > 0 {
		adherence += " Constraints: " + strings.Join(constraints, "; ") + "."
	}

	in := ai.JudgeInput{
		Task: challengeTask(c),
		Criteria: []ai.JudgeCriterion{
			{Name: criterionAccuracy, MaxScore: domain.ScoreScale, Description: "How fully the output achieves the task, judged against the challenge's rubric:" + rubric.String()},
			{Name: criterionSimplicity, MaxScore: domain.ScoreScale, Description: "How clear, direct and economical the prompt is."},
			{Name: criterionCreativity, MaxScore: domain.ScoreScale, Description: "How original and inventive the prompt's approach is."},
			{Name: criterionAdherence, MaxScore: domain.ScoreScale, Description: adherence},
		},
	}
	for _, l := range labeled {
		in.Candidates = append(in.Candidates, ai.JudgeCandidate{ID: l.Label, Prompt: l.Prompt, Output: l.Output})
	}
	return in
}

// ScoreRound turns the judge's verdict into every player's score and rank.
// out.Results follow the order of labeled; players without a submission get
// a zero score, ranked last.
func ScoreRound(game *domain.Game, roundID uuid.UUID, labeled []LabeledSubmission, out ai.JudgeOutput, weights domain.ScoreWeights, now time.Time) []domain.Score {
	scores := make([]domain.Score, 0, len(game.Players))
	entries := make([]domain.RankEntry, 0, len(game.Players))
	submitted := map[uuid.UUID]bool{}
	for i, l := range labeled {
		r := out.Results[i]
		c := domain.ScoreComponents{
			Accuracy:   domain.PointsFrom(r.Scores[criterionAccuracy]),
			Simplicity: domain.PointsFrom(r.Scores[criterionSimplicity]),
			Creativity: domain.PointsFrom(r.Scores[criterionCreativity]),
			Adherence:  domain.PointsFrom(r.Scores[criterionAdherence]),
			Efficiency: domain.Efficiency(l.PromptLength, l.TestCount),
		}
		s := domain.Score{
			GameID: game.ID, RoundID: roundID, PlayerID: l.PlayerID, SubmissionID: l.ID, Label: l.Label,
			Components: c, Total: domain.Total(c, weights), Rationale: r.Feedback,
			Provider: out.Provider, Model: out.Model, CreatedAt: now,
		}
		scores = append(scores, s)
		entries = append(entries, domain.RankEntry{PlayerID: l.PlayerID, Total: s.Total, Accuracy: c.Accuracy,
			TestCount: l.TestCount, PromptLength: l.PromptLength, SubmittedAt: l.SubmittedAt})
		submitted[l.PlayerID] = true
	}
	for _, p := range game.Players {
		if !submitted[p.UserID] {
			scores = append(scores, domain.Score{GameID: game.ID, RoundID: roundID, PlayerID: p.UserID, NoSubmission: true, CreatedAt: now})
			entries = append(entries, domain.RankEntry{PlayerID: p.UserID, NoSubmission: true})
		}
	}
	for i, rank := range domain.Rank(entries) {
		scores[i].Rank = rank
	}
	return scores
}

// judgingError names a failure and says whether retrying may help. Invalid
// judge output is retried: the model may do better the next time.
func judgingError(err error) (string, bool) {
	if errors.Is(err, errNoChallenge) {
		return JudgingErrorNoChallenge, false
	}
	var aerr *ai.AIError
	if errors.As(err, &aerr) {
		return strings.ToUpper(string(aerr.Kind)), aerr.Retryable() || aerr.Kind == ai.ErrKindInvalidOutput
	}
	return "INTERNAL", true
}

// challengeTask describes the challenge to the judge.
func challengeTask(c *domain.Challenge) string {
	return fmt.Sprintf("%s\n\n%s", c.Title, c.Description)
}

// sleepCtx waits d, returning early with ctx's error once ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
