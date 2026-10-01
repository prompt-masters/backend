package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
	"github.com/prompt-masters/backend/internal/ws"
)

// persistTimeout bounds the write that records how a test ended; it runs
// even when the test's own context is already done.
const persistTimeout = 5 * time.Second

// Error codes stored with a test that did not complete, besides the
// upper-cased ai.ErrorKind of a failure.
const (
	TestErrorRoundEnded  = "ROUND_ENDED"
	TestErrorCanceled    = "CANCELED"
	TestErrorInterrupted = "INTERRUPTED"
)

// PromptExecutor streams a prompt's output; *ai.Orchestrator satisfies it.
type PromptExecutor interface {
	ExecuteStream(ctx context.Context, in ai.ExecuteInput) (<-chan ai.StreamEvent, error)
	ExecutorTarget(model string) (ai.Target, error)
}

// PromptTestService runs players' prompt tests during a round. A test is
// validated and stored in one transaction that holds the game's row lock, the
// lock every engine transition takes, so it sees the round exactly as the
// engine does and numbers stay gap-free. The AI call runs afterwards, on its
// own goroutine, with no lock held; its output streams only to the player
// who ran it, and the row is updated when it ends. A test never outlives its
// round's deadline.
type PromptTestService struct {
	tx     repository.Transactor
	ai     PromptExecutor
	hub    EventBroadcaster
	clock  Clock
	logger *log.Logger
	cfg    config.PromptTestConfig

	background context.Context
	stop       context.CancelFunc

	mu      sync.Mutex
	closed  bool
	running sync.WaitGroup
}

// NewPromptTestService takes executor as nil when AI is disabled; tests are
// then refused with domain.ErrAIUnavailable.
func NewPromptTestService(tx repository.Transactor, executor PromptExecutor, hub EventBroadcaster, clock Clock, logger *log.Logger, cfg config.PromptTestConfig) *PromptTestService {
	background, stop := context.WithCancel(context.Background())
	return &PromptTestService{tx: tx, ai: executor, hub: hub, clock: clock, logger: logger, cfg: cfg, background: background, stop: stop}
}

// Start stores a pending test of prompt for userID in the round and runs it
// in the background. It returns once the test is stored.
func (s *PromptTestService) Start(ctx context.Context, userID uuid.UUID, roundRef, prompt string) (*domain.PromptTest, error) {
	if msg := s.validatePrompt(prompt); msg != "" {
		return nil, &ValidationError{Fields: map[string]string{"prompt": msg}}
	}
	if s.ai == nil {
		return nil, domain.ErrAIUnavailable
	}
	roundID, err := uuid.Parse(roundRef)
	if err != nil {
		return nil, domain.ErrNotFound
	}

	var (
		test  *domain.PromptTest
		model domain.AIModel
		left  time.Duration
	)
	err = s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		round, err := repos.Games.GetRound(ctx, roundID)
		if err != nil {
			return err
		}
		game, err := repos.Games.LockByID(ctx, round.GameID)
		if err != nil {
			return err
		}
		if !game.HasPlayer(userID) {
			return domain.ErrNotGamePlayer
		}
		now := s.clock.Now() // read under the lock
		p := game.Progress
		if game.Status != domain.GameStatusInProgress || p.RoundID != roundID || !p.AcceptsSubmissions(now) {
			return domain.ErrRoundNotActive
		}
		running, err := repos.PromptTests.CountUnfinished(ctx, roundID, userID, now.Add(-s.cfg.StaleAfter))
		if err != nil {
			return err
		}
		if running >= s.cfg.MaxConcurrent {
			return domain.ErrTestInProgress
		}
		number, err := repos.PromptTests.NextNumber(ctx, roundID, userID)
		if err != nil {
			return err
		}
		test = &domain.PromptTest{
			GameID: game.ID, RoundID: roundID, PlayerID: userID, TestNumber: number,
			Prompt: prompt, Status: domain.PromptTestPending, CreatedAt: now,
		}
		model, left = game.Settings.AIModel, p.RoundDeadline.Sub(now)
		return repos.PromptTests.Create(ctx, test)
	})
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		s.finish(*test, domain.PromptTestResult{Status: domain.PromptTestCanceled, ErrorCode: TestErrorCanceled, CompletedAt: s.clock.Now()})
		test.Status = domain.PromptTestCanceled
		return test, nil
	}
	s.running.Add(1)
	s.mu.Unlock()
	go s.run(*test, model, left)
	return test, nil
}

// List returns userID's own tests in the round, by test number. No other
// player's tests are ever returned.
func (s *PromptTestService) List(ctx context.Context, userID uuid.UUID, roundRef string) ([]domain.PromptTest, error) {
	roundID, err := uuid.Parse(roundRef)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	var tests []domain.PromptTest
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
		tests, err = repos.PromptTests.ListByRoundAndPlayer(ctx, roundID, userID)
		return err
	})
	return tests, err
}

// Recover fails the tests a stopped server left unfinished, so they stop
// counting against the concurrency limit. They keep their numbers.
func (s *PromptTestService) Recover(ctx context.Context) error {
	now := s.clock.Now()
	var n int64
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		n, err = repos.PromptTests.FailStale(ctx, now.Add(-s.cfg.StaleAfter), now)
		return err
	})
	if err == nil && n > 0 {
		s.logger.Printf("prompt_test op=recover interrupted=%d", n)
	}
	return err
}

// Close cancels running tests and waits until each has been recorded.
func (s *PromptTestService) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	s.running.Wait()
}

func (s *PromptTestService) validatePrompt(prompt string) string {
	switch {
	case strings.TrimSpace(prompt) == "":
		return "is required"
	case len(prompt) > s.cfg.MaxPromptBytes:
		return fmt.Sprintf("must not exceed %d bytes", s.cfg.MaxPromptBytes)
	case !utf8.ValidString(prompt):
		return "must be valid UTF-8"
	}
	return ""
}

// run streams one test to its player and records how it ended. left is the
// time until the round's deadline.
func (s *PromptTestService) run(test domain.PromptTest, model domain.AIModel, left time.Duration) {
	defer s.running.Done()
	ctx, cancel := context.WithTimeout(s.background, left)
	defer cancel()
	started := time.Now()

	result := domain.PromptTestResult{}
	if target, err := s.ai.ExecutorTarget(string(model)); err == nil {
		result.Provider, result.Model = target.Provider, target.Model
	}
	s.persist("mark_streaming", test, func(ctx context.Context, repos repository.TxRepositories) error {
		return repos.PromptTests.MarkStreaming(ctx, test.ID, s.clock.Now())
	})

	var (
		output strings.Builder
		final  ai.StreamEvent
		failed error
	)
	events, err := s.ai.ExecuteStream(ctx, ai.ExecuteInput{
		Model:  string(model),
		Prompt: test.Prompt,
		Metadata: map[string]string{
			"game_id": test.GameID.String(), "round_id": test.RoundID.String(),
			"user_id": test.PlayerID.String(), "test_id": test.ID.String(),
		},
	})
	if err != nil {
		failed = err
	} else {
		seq := 0
		for ev := range events {
			switch {
			case ev.Err != nil:
				failed = ev.Err
			case ev.Done:
				final = ev
			case ev.Delta != "":
				seq++
				output.WriteString(ev.Delta)
				s.send(test, ws.OutputChunkPayload{TestID: test.ID.String(), TestNumber: test.TestNumber, Seq: seq, Delta: ev.Delta})
			}
		}
		if failed == nil && !final.Done {
			failed = cmpErr(ai.ContextError(ctx, result.Provider), errors.New("stream closed without a result"))
		}
	}

	latency := time.Since(started).Milliseconds()
	result.LatencyMs = &latency
	result.CompletedAt = s.clock.Now()
	if output.Len() > 0 || failed == nil {
		text := output.String()
		result.Output = &text // a failed test keeps its partial output
	}
	if failed == nil {
		result.Status = domain.PromptTestCompleted
		if u := final.Usage; u != nil {
			result.InputTokens, result.OutputTokens, result.CostUSD = &u.InputTokens, &u.OutputTokens, u.CostUSD
		}
	} else {
		result.Status, result.ErrorCode = s.classify(ctx, failed)
	}
	s.finish(test, result)
}

// classify decides how a test that did not complete ended: canceled by
// shutdown or by its round's deadline, or failed with the AI error's kind.
func (s *PromptTestService) classify(ctx context.Context, err error) (domain.PromptTestStatus, string) {
	switch {
	case s.background.Err() != nil:
		return domain.PromptTestCanceled, TestErrorCanceled
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return domain.PromptTestCanceled, TestErrorRoundEnded
	}
	return domain.PromptTestFailed, strings.ToUpper(string(ai.KindOf(err)))
}

// finish records the result, then announces it to the player: what the
// player is told is what was stored.
func (s *PromptTestService) finish(test domain.PromptTest, result domain.PromptTestResult) {
	s.persist("finish", test, func(ctx context.Context, repos repository.TxRepositories) error {
		return repos.PromptTests.Finish(ctx, test.ID, result)
	})
	s.logger.Printf("prompt_test op=finish game_id=%s round_id=%s user_id=%s test_id=%s test_number=%d status=%s error_code=%s latency_ms=%d",
		test.GameID, test.RoundID, test.PlayerID, test.ID, test.TestNumber, result.Status, result.ErrorCode, derefInt64(result.LatencyMs))

	if result.Status == domain.PromptTestCompleted {
		payload := ws.OutputCompletePayload{TestID: test.ID.String(), TestNumber: test.TestNumber, Status: string(result.Status)}
		if result.Output != nil {
			payload.Output = *result.Output
		}
		if result.InputTokens != nil && result.OutputTokens != nil {
			payload.Usage = &ws.UsageInfo{InputTokens: *result.InputTokens, OutputTokens: *result.OutputTokens}
		}
		s.send(test, payload)
		return
	}
	s.send(test, ws.OutputErrorPayload{
		TestID: test.ID.String(), TestNumber: test.TestNumber, Status: string(result.Status),
		ErrorCode: result.ErrorCode, Message: testErrorMessage(result.ErrorCode),
	})
}

// persist runs a write on its own short context, since the test's context
// may already be done. Failures are logged: the test still reaches the player.
func (s *PromptTestService) persist(op string, test domain.PromptTest, write func(context.Context, repository.TxRepositories) error) {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error { return write(ctx, repos) })
	if err != nil {
		s.logger.Printf("prompt_test op=%s test_id=%s error=%q", op, test.ID, err)
	}
}

// send delivers an event to every connection of the test's player, and to
// no one else.
func (s *PromptTestService) send(test domain.PromptTest, payload ws.Payload) {
	msg, err := ws.Encode(payload)
	if err != nil {
		s.logger.Printf("prompt_test op=encode event=%s test_id=%s error=%q", payload.EventType(), test.ID, err)
		return
	}
	s.hub.SendToUser(test.GameID, test.PlayerID, msg)
}

// testErrorMessage is the text a player sees; provider detail stays in logs.
func testErrorMessage(code string) string {
	switch code {
	case TestErrorRoundEnded:
		return "The round ended before the test finished."
	case TestErrorCanceled:
		return "The test was stopped because the server is restarting. Please try again."
	case strings.ToUpper(string(ai.ErrKindTimeout)):
		return "The AI took too long to answer."
	case strings.ToUpper(string(ai.ErrKindRateLimit)), strings.ToUpper(string(ai.ErrKindOverloaded)), strings.ToUpper(string(ai.ErrKindUnavailable)):
		return "The AI is busy right now. Please try again in a moment."
	case strings.ToUpper(string(ai.ErrKindContentFiltered)):
		return "The AI declined to answer this prompt."
	case strings.ToUpper(string(ai.ErrKindInvalidRequest)):
		return "The AI could not process this prompt."
	}
	return "The test failed. Please try again."
}

func cmpErr(a *ai.AIError, b error) error {
	if a != nil {
		return a
	}
	return b
}

func derefInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
