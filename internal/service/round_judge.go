package service

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// maxParallelExecutions bounds the executor calls one round makes at once.
const maxParallelExecutions = 4

// noOutput stands in for a submission whose prompt could not be run.
const noOutput = "[the model produced no output for this prompt]"

// AIRunner is the part of the AI layer judging needs; *ai.Orchestrator
// satisfies it.
type AIRunner interface {
	Execute(ctx context.Context, in ai.ExecuteInput) (ai.ExecuteOutput, error)
	Judge(ctx context.Context, in ai.JudgeInput) (ai.JudgeOutput, error)
}

// RoundChallenges finds the challenge a round was played on.
type RoundChallenges interface {
	ChallengeForRound(ctx context.Context, gameID, roundID uuid.UUID) (*domain.Challenge, error)
}

// RoundScore is one player's judged result.
type RoundScore struct {
	UserID uuid.UUID
	Output string
	// Scores are the judge's raw scores by criterion name.
	Scores map[string]float64
	// Total is the rubric-weighted score on a 0-100 scale.
	Total    float64
	Feedback string
}

// RoundJudge judges a round: it runs every submitted prompt through the
// executor, has the judge score the outputs against the challenge's rubric,
// and then completes judging in the engine. It is the engine's RoundJudger.
//
// Rounds do not yet record the challenge they were played on, so until a
// RoundChallenges source is wired in, JudgeRound logs and leaves the round
// in Judging.
type RoundJudge struct {
	ai         AIRunner
	tx         repository.Transactor
	challenges RoundChallenges
	engine     *GameEngine
	logger     *log.Logger
}

// NewRoundJudge takes challenges as nil while rounds carry no challenge.
func NewRoundJudge(runner AIRunner, tx repository.Transactor, challenges RoundChallenges, engine *GameEngine, logger *log.Logger) *RoundJudge {
	return &RoundJudge{ai: runner, tx: tx, challenges: challenges, engine: engine, logger: logger}
}

var _ RoundJudger = (*RoundJudge)(nil)

// JudgeRound runs outside the game's lock, on the engine's background
// context. A judging failure is logged and the round still moves to Results,
// so a game is never stuck on the AI; only shutdown leaves it in Judging, for
// Recover to judge again.
func (j *RoundJudge) JudgeRound(ctx context.Context, gameID, roundID uuid.UUID) {
	if j.challenges == nil {
		j.logger.Printf("judge op=skip game_id=%s round_id=%s reason=rounds_have_no_challenge", gameID, roundID)
		return
	}
	challenge, err := j.challenges.ChallengeForRound(ctx, gameID, roundID)
	if err != nil {
		j.logger.Printf("judge op=challenge game_id=%s round_id=%s error=%q", gameID, roundID, err)
		return
	}

	var (
		game *domain.Game
		subs []domain.Submission
	)
	err = j.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		if game, err = repos.Games.GetByID(ctx, gameID); err != nil {
			return err
		}
		subs, err = repos.Submissions.ListByRound(ctx, roundID)
		return err
	})
	if err != nil {
		j.logger.Printf("judge op=load game_id=%s round_id=%s error=%q", gameID, roundID, err)
		return
	}

	scores, err := j.Score(ctx, game, roundID, challenge, subs)
	if ctx.Err() != nil {
		return // shutting down: Recover judges the round again
	}
	if err != nil {
		j.logger.Printf("judge op=score game_id=%s round_id=%s error_kind=%s error=%q", gameID, roundID, ai.KindOf(err), err)
	}
	for _, s := range scores {
		j.logger.Printf("judge op=scored game_id=%s round_id=%s user_id=%s total=%.1f", gameID, roundID, s.UserID, s.Total)
	}
	if err := j.engine.CompleteJudging(ctx, gameID, roundID); err != nil {
		j.logger.Printf("judge op=complete game_id=%s round_id=%s error=%q", gameID, roundID, err)
	}
}

// Score runs each submission's prompt with the game's model, then judges the
// outputs. A prompt that fails to run is judged with no output rather than
// failing the round. Results follow the order of subs.
func (j *RoundJudge) Score(ctx context.Context, game *domain.Game, roundID uuid.UUID, challenge *domain.Challenge, subs []domain.Submission) ([]RoundScore, error) {
	if len(subs) == 0 {
		return nil, nil
	}
	metadata := func(userID uuid.UUID) map[string]string {
		m := map[string]string{"game_id": game.ID.String(), "round_id": roundID.String()}
		if userID != uuid.Nil {
			m["user_id"] = userID.String()
		}
		return m
	}

	outputs := make([]string, len(subs))
	slots := make(chan struct{}, maxParallelExecutions)
	var wg sync.WaitGroup
	for i, sub := range subs {
		wg.Go(func() {
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			case <-ctx.Done():
				return
			}
			out, err := j.ai.Execute(ctx, ai.ExecuteInput{
				Model:    string(game.Settings.AIModel),
				Prompt:   sub.Prompt,
				Metadata: metadata(sub.UserID),
			})
			if err != nil {
				j.logger.Printf("judge op=execute game_id=%s round_id=%s user_id=%s error_kind=%s", game.ID, roundID, sub.UserID, ai.KindOf(err))
				return
			}
			outputs[i] = out.Text
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	criteria := challenge.JudgeCriteria.Criteria
	in := ai.JudgeInput{Task: challengeTask(challenge), Metadata: metadata(uuid.Nil)}
	for _, c := range criteria {
		in.Criteria = append(in.Criteria, ai.JudgeCriterion{Name: c.Name, Description: c.Description, MaxScore: c.MaxScore})
	}
	for i, sub := range subs {
		in.Candidates = append(in.Candidates, ai.JudgeCandidate{ID: sub.UserID.String(), Prompt: sub.Prompt, Output: cmpOrText(outputs[i])})
	}
	judged, err := j.ai.Judge(ctx, in)
	if err != nil {
		return nil, err
	}

	scores := make([]RoundScore, len(subs))
	for i, r := range judged.Results {
		scores[i] = RoundScore{
			UserID:   subs[i].UserID,
			Output:   outputs[i],
			Scores:   r.Scores,
			Total:    weightedTotal(criteria, r.Scores),
			Feedback: r.Feedback,
		}
	}
	return scores, nil
}

// weightedTotal turns raw criterion scores into the rubric's 0-100 score.
func weightedTotal(criteria []domain.JudgeCriterion, scores map[string]float64) float64 {
	var total float64
	for _, c := range criteria {
		total += c.Weight * scores[c.Name] / float64(c.MaxScore)
	}
	return total * 100
}

// challengeTask describes the challenge to the judge.
func challengeTask(c *domain.Challenge) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n%s", c.Title, c.Description)
	if len(c.Constraints.RequiredElements) > 0 {
		fmt.Fprintf(&b, "\n\nThe output must include: %s.", strings.Join(c.Constraints.RequiredElements, ", "))
	}
	if c.Constraints.OutputFormat != "" {
		fmt.Fprintf(&b, "\nExpected output format: %s.", c.Constraints.OutputFormat)
	}
	return b.String()
}

func cmpOrText(output string) string {
	if strings.TrimSpace(output) == "" {
		return noOutput
	}
	return output
}
