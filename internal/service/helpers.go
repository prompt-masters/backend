package service

import (
	"context"
	"encoding/json"
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
)

// Helper error codes, besides the upper-cased ai.ErrorKind of an AI failure.
const (
	HelperErrorRoundEnded    = "ROUND_ENDED"
	HelperErrorTimeout       = "TIMEOUT"
	HelperErrorInvalidOutput = "INVALID_OUTPUT"
	HelperErrorPersist       = "PERSIST_FAILED"
	HelperErrorAbandoned     = "ABANDONED"
)

// recoveryMargin is how long past the helper timeout a reservation may stay
// before recovery refunds it.
const recoveryMargin = 30 * time.Second

var (
	errRoundEnded    = errors.New("the round ended during the helper call")
	errHelperTimeout = errors.New("the helper took too long")
	errKeyTaken      = errors.New("idempotency key already used")
)

// HelperAI is the part of the AI layer the helpers need; *ai.Orchestrator
// satisfies it.
type HelperAI interface {
	Execute(ctx context.Context, in ai.ExecuteInput) (ai.ExecuteOutput, error)
}

// InsufficientBalanceError refuses a helper the player cannot afford.
type InsufficientBalanceError struct {
	Balance int
	Cost    int
}

func (e *InsufficientBalanceError) Error() string {
	return fmt.Sprintf("helper costs %d, balance is %d", e.Cost, e.Balance)
}

func (e *InsufficientBalanceError) Unwrap() error { return domain.ErrInsufficientBalance }

// HelperFailedError reports a helper that could not be delivered. Its points
// have been given back.
type HelperFailedError struct {
	Code    string
	Balance int
}

func (e *HelperFailedError) Error() string { return "helper failed: " + e.Code }

// HelperRequest is a player's request for a helper.
type HelperRequest struct {
	Type     domain.HelperType
	RoundRef string
	// Prompt is the player's current draft.
	Prompt string
	// TestRef optionally names one of the player's completed tests to review.
	TestRef        string
	IdempotencyKey string
}

// HelperResult is a delivered helper and the balance after it.
type HelperResult struct {
	Usage   domain.HelperUsage
	Balance int
}

// HelperOverview is what a player sees of their helpers in a round.
type HelperOverview struct {
	Balance int
	Prices  map[domain.HelperType]int
	Usage   []domain.HelperUsage
}

// HelperService sells the in-round AI helpers. A purchase reserves the points
// and records the usage in one short transaction, calls the AI with no
// transaction or lock held, then either delivers (marking the usage
// delivered in a transaction, and only then returning the text) or refunds
// (giving the points back, at most once). A player is never charged for a
// helper they did not receive, nor twice for one request: a request repeated
// with the same idempotency key returns the first one's outcome.
//
// Balances live in PostgreSQL, not Redis: the debit and the usage row must
// commit together, and Redis here runs without persistence.
type HelperService struct {
	tx     repository.Transactor
	ai     HelperAI
	clock  Clock
	logger *log.Logger
	cfg    config.HelperConfig

	background context.Context
	stop       context.CancelFunc

	mu       sync.Mutex
	closed   bool
	inflight map[uuid.UUID]map[uuid.UUID]context.CancelCauseFunc // round → usage → cancel
	workers  sync.WaitGroup
}

// NewHelperService takes helperAI as nil when AI is disabled; purchases are then
// refused with domain.ErrAIUnavailable before anything is charged.
func NewHelperService(tx repository.Transactor, helperAI HelperAI, clock Clock, logger *log.Logger, cfg config.HelperConfig) *HelperService {
	background, stop := context.WithCancel(context.Background())
	return &HelperService{
		tx: tx, ai: helperAI, clock: clock, logger: logger, cfg: cfg,
		background: background, stop: stop,
		inflight: map[uuid.UUID]map[uuid.UUID]context.CancelCauseFunc{},
	}
}

// Use buys and runs a helper. See HelperService for the guarantees.
func (s *HelperService) Use(ctx context.Context, userID uuid.UUID, req HelperRequest) (*HelperResult, error) {
	if err := s.validate(req); err != nil {
		return nil, err
	}
	if s.ai == nil {
		return nil, domain.ErrAIUnavailable
	}
	roundID, err := uuid.Parse(req.RoundRef)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	var testID uuid.UUID
	if req.TestRef != "" {
		if testID, err = uuid.Parse(req.TestRef); err != nil {
			return nil, domain.ErrInvalidTest
		}
	}

	r, err := s.reserve(ctx, userID, roundID, testID, req)
	if errors.Is(err, errKeyTaken) {
		// A concurrent request with the same key won the insert.
		return s.replayKey(ctx, userID, req.IdempotencyKey)
	}
	if err != nil {
		return nil, err
	}
	if r.replay != nil {
		return s.replay(ctx, r.replay)
	}
	return s.run(ctx, r)
}

// reservation is a reserved helper with everything its AI call needs.
type reservation struct {
	usage    domain.HelperUsage
	input    string
	deadline time.Time
	replay   *domain.HelperUsage
}

// reserve validates the request against the game and takes the points, in
// one transaction. The game is share-locked, so no engine transition can
// happen meanwhile, while other players' purchases proceed in parallel.
func (s *HelperService) reserve(ctx context.Context, userID, roundID, testID uuid.UUID, req HelperRequest) (*reservation, error) {
	var r reservation
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		round, err := repos.Games.GetRound(ctx, roundID)
		if err != nil {
			return err
		}
		game, err := repos.Games.LockSharedByID(ctx, round.GameID)
		if err != nil {
			return err
		}
		if !game.HasPlayer(userID) {
			return domain.ErrNotGamePlayer
		}
		if existing, err := repos.Helpers.GetUsageByKey(ctx, userID, req.IdempotencyKey); err == nil {
			r.replay = existing
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		now := s.clock.Now()
		p := game.Progress
		if game.Status != domain.GameStatusInProgress || p.RoundID != roundID || !p.AcceptsSubmissions(now) {
			return domain.ErrRoundNotActive
		}
		if _, err := repos.Submissions.GetForPlayer(ctx, roundID, userID); err == nil {
			return domain.ErrAlreadySubmitted
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}

		prompt, output := req.Prompt, ""
		if testID != uuid.Nil {
			test, err := repos.PromptTests.Get(ctx, testID)
			if errors.Is(err, domain.ErrNotFound) || (err == nil && (test.PlayerID != userID || test.RoundID != roundID || test.Status != domain.PromptTestCompleted)) {
				return domain.ErrInvalidTest
			}
			if err != nil {
				return err
			}
			if strings.TrimSpace(prompt) == "" {
				prompt = test.Prompt
			}
			if test.Output != nil {
				output = *test.Output
			}
		}
		if req.Type == domain.HelperReview && strings.TrimSpace(prompt) == "" {
			return &ValidationError{Fields: map[string]string{"prompt": "a review needs a prompt or a test_id"}}
		}

		var challenge *domain.Challenge
		if round.ChallengeID != uuid.Nil {
			if challenge, err = repos.Challenges.GetByID(ctx, round.ChallengeID); err != nil {
				return err
			}
		}

		cost := s.cfg.Costs[req.Type]
		if err := repos.Helpers.EnsureBalance(ctx, game.ID, userID, s.cfg.StartingBalance, now); err != nil {
			return err
		}
		r.usage = domain.HelperUsage{
			GameID: game.ID, RoundID: roundID, PlayerID: userID, Type: req.Type, Cost: cost,
			Status: domain.HelperReserved, IdempotencyKey: req.IdempotencyKey, InputPrompt: req.Prompt,
			TestID: testID, CreatedAt: now,
		}
		created, err := repos.Helpers.CreateUsage(ctx, &r.usage)
		if err != nil {
			return err
		}
		if !created {
			return errKeyTaken
		}
		if _, ok, err := repos.Helpers.Debit(ctx, game.ID, userID, cost, now); err != nil {
			return err
		} else if !ok {
			balance, err := repos.Helpers.GetBalance(ctx, game.ID, userID)
			if err != nil {
				return err
			}
			return &InsufficientBalanceError{Balance: balance, Cost: cost}
		}
		r.input = helperInput(challenge, req.Type, prompt, output)
		r.deadline = p.RoundDeadline
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// run calls the AI for a reservation, then delivers or refunds it.
func (s *HelperService) run(ctx context.Context, r *reservation) (*HelperResult, error) {
	u := r.usage
	ctx, done, ok := s.track(ctx, u, r.deadline)
	if !ok {
		return nil, s.refund(u, HelperErrorAbandoned)
	}
	defer done()

	tmpl := s.cfg.Templates[u.Type]
	started := time.Now()
	out, err := s.ai.Execute(ctx, ai.ExecuteInput{
		System: tmpl.System,
		Prompt: r.input,
		Metadata: map[string]string{
			"game_id": u.GameID.String(), "round_id": u.RoundID.String(), "user_id": u.PlayerID.String(),
			"helper": string(u.Type), "usage_id": u.ID.String(),
		},
	})
	s.logger.Printf("helper op=call usage_id=%s helper=%s template=%s game_id=%s round_id=%s user_id=%s latency_ms=%d error_kind=%s",
		u.ID, u.Type, tmpl.Version, u.GameID, u.RoundID, u.PlayerID, time.Since(started).Milliseconds(), ai.KindOf(err))
	if err != nil {
		return nil, s.refund(u, helperErrorCode(ctx, err))
	}
	content := strings.TrimSpace(out.Text)
	if content == "" {
		return nil, s.refund(u, HelperErrorInvalidOutput)
	}
	content = truncateRunes(content, s.cfg.MaxOutputChars)

	delivery := domain.HelperDelivery{
		Content: content, Provider: out.Provider, Model: out.Model,
		InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, CostUSD: out.Usage.CostUSD,
		DeliveredAt: s.clock.Now(),
	}
	// Delivery is recorded on its own context: the call is over, and a
	// round that ended meanwhile is detected below rather than by a failed
	// write.
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), persistTimeout)
	defer cancel()
	var balance int
	err = s.tx.WithinTx(dctx, func(repos repository.TxRepositories) error {
		game, err := repos.Games.LockSharedByID(dctx, u.GameID)
		if err != nil {
			return err
		}
		if p := game.Progress; game.Status != domain.GameStatusInProgress || p.Phase != domain.GamePhaseRoundActive || p.RoundID != u.RoundID {
			return errRoundEnded
		}
		if err := repos.Helpers.Deliver(dctx, u.ID, delivery); err != nil {
			return err
		}
		balance, err = repos.Helpers.GetBalance(dctx, u.GameID, u.PlayerID)
		return err
	})
	if errors.Is(err, errRoundEnded) {
		return nil, s.refund(u, HelperErrorRoundEnded)
	}
	if err != nil {
		s.logger.Printf("helper op=deliver usage_id=%s error=%q", u.ID, err)
		return nil, s.refund(u, HelperErrorPersist)
	}
	u.Status, u.Content, u.DeliveredAt = domain.HelperDelivered, content, &delivery.DeliveredAt
	return &HelperResult{Usage: u, Balance: balance}, nil
}

// refund gives a reservation's points back, once, and returns the error to
// report: a HelperFailedError carrying the restored balance.
func (s *HelperService) refund(u domain.HelperUsage, code string) error {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	var balance int
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		balance, err = s.refundIn(ctx, repos, u, code)
		return err
	})
	if err != nil {
		// Recovery refunds it later; the player was not served.
		s.logger.Printf("helper op=refund usage_id=%s error=%q", u.ID, err)
	}
	return &HelperFailedError{Code: code, Balance: balance}
}

// refundIn marks u refunded and credits its cost, only if it was still
// reserved, so a refund never happens twice.
func (s *HelperService) refundIn(ctx context.Context, repos repository.TxRepositories, u domain.HelperUsage, code string) (int, error) {
	now := s.clock.Now()
	flipped, err := repos.Helpers.MarkRefunded(ctx, u.ID, code, now)
	if err != nil {
		return 0, err
	}
	if flipped {
		s.logger.Printf("helper op=refunded usage_id=%s helper=%s cost=%d error_code=%s", u.ID, u.Type, u.Cost, code)
		return repos.Helpers.Credit(ctx, u.GameID, u.PlayerID, u.Cost, now)
	}
	return repos.Helpers.GetBalance(ctx, u.GameID, u.PlayerID)
}

// replay answers a repeated request with the first one's outcome.
func (s *HelperService) replay(ctx context.Context, u *domain.HelperUsage) (*HelperResult, error) {
	var balance int
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		balance, err = repos.Helpers.GetBalance(ctx, u.GameID, u.PlayerID)
		return err
	})
	if err != nil {
		return nil, err
	}
	switch u.Status {
	case domain.HelperDelivered:
		return &HelperResult{Usage: *u, Balance: balance}, nil
	case domain.HelperRefunded:
		return nil, &HelperFailedError{Code: u.ErrorCode, Balance: balance}
	}
	return nil, domain.ErrHelperInProgress
}

func (s *HelperService) replayKey(ctx context.Context, userID uuid.UUID, key string) (*HelperResult, error) {
	var u *domain.HelperUsage
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		u, err = repos.Helpers.GetUsageByKey(ctx, userID, key)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.replay(ctx, u)
}

// Overview returns the player's balance, the prices and their own helper
// usage in the round. No other player's usage is ever included.
func (s *HelperService) Overview(ctx context.Context, userID uuid.UUID, roundRef string) (*HelperOverview, error) {
	roundID, err := uuid.Parse(roundRef)
	if err != nil {
		return nil, domain.ErrNotFound
	}
	o := &HelperOverview{Prices: s.cfg.Costs}
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
		o.Balance, err = repos.Helpers.GetBalance(ctx, game.ID, userID)
		if errors.Is(err, domain.ErrNotFound) {
			o.Balance = s.cfg.StartingBalance // nothing spent yet
		} else if err != nil {
			return err
		}
		o.Usage, err = repos.Helpers.ListUsage(ctx, roundID, userID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return o, nil
}

// RoundEnded cancels the helper calls still running in the round; they are
// refunded. It is the engine's OnRoundEnd listener, so it only signals.
func (s *HelperService) RoundEnded(_, roundID uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, cancel := range s.inflight[roundID] {
		cancel(errRoundEnded)
	}
}

// RecoverStale refunds reservations older than the helper timeout plus a
// margin: their request must have died before delivering or refunding. Each
// refund happens at most once, however many recoveries run.
func (s *HelperService) RecoverStale(ctx context.Context) (int, error) {
	var stale []domain.HelperUsage
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		stale, err = repos.Helpers.ListStaleReservations(ctx, s.clock.Now().Add(-s.cfg.Timeout-recoveryMargin))
		return err
	})
	if err != nil {
		return 0, err
	}
	refunded := 0
	for _, u := range stale {
		err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
			flipped, err := repos.Helpers.MarkRefunded(ctx, u.ID, HelperErrorAbandoned, s.clock.Now())
			if err != nil || !flipped {
				return err
			}
			refunded++
			_, err = repos.Helpers.Credit(ctx, u.GameID, u.PlayerID, u.Cost, s.clock.Now())
			return err
		})
		if err != nil {
			return refunded, err
		}
	}
	if refunded > 0 {
		s.logger.Printf("helper op=recover refunded=%d", refunded)
	}
	return refunded, nil
}

// StartRecovery refunds stale reservations now and then every
// cfg.RecoveryInterval until Close.
func (s *HelperService) StartRecovery() {
	s.workers.Go(func() {
		ticker := time.NewTicker(s.cfg.RecoveryInterval)
		defer ticker.Stop()
		for {
			if _, err := s.RecoverStale(s.background); err != nil && s.background.Err() == nil {
				s.logger.Printf("helper op=recover error=%q", err)
			}
			select {
			case <-ticker.C:
			case <-s.background.Done():
				return
			}
		}
	})
}

// Close cancels running helper calls (they are refunded) and stops recovery.
func (s *HelperService) Close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	s.stop()
	s.workers.Wait()
}

// track bounds a helper call by the helper timeout and the round's deadline,
// and lets RoundEnded and Close cancel it.
func (s *HelperService) track(parent context.Context, u domain.HelperUsage, deadline time.Time) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, false
	}
	ctx, cancel := context.WithCancelCause(parent)
	stopOnClose := context.AfterFunc(s.background, func() { cancel(context.Canceled) })
	ctx, cancelRound := context.WithTimeoutCause(ctx, deadline.Sub(s.clock.Now()), errRoundEnded)
	ctx, cancelTimeout := context.WithTimeoutCause(ctx, s.cfg.Timeout, errHelperTimeout)
	if s.inflight[u.RoundID] == nil {
		s.inflight[u.RoundID] = map[uuid.UUID]context.CancelCauseFunc{}
	}
	s.inflight[u.RoundID][u.ID] = cancel
	s.workers.Add(1)
	return ctx, func() {
		cancelTimeout()
		cancelRound()
		stopOnClose()
		cancel(nil)
		s.mu.Lock()
		delete(s.inflight[u.RoundID], u.ID)
		if len(s.inflight[u.RoundID]) == 0 {
			delete(s.inflight, u.RoundID)
		}
		s.mu.Unlock()
		s.workers.Done()
	}, true
}

func (s *HelperService) validate(req HelperRequest) error {
	fields := map[string]string{}
	if !req.Type.Valid() {
		fields["helper_type"] = "must be hint, suggestion or review"
	}
	if k := req.IdempotencyKey; k == "" || len(k) > 128 {
		fields["idempotency_key"] = "an Idempotency-Key header of 1 to 128 characters is required"
	}
	switch {
	case len(req.Prompt) > s.cfg.MaxInputBytes:
		fields["prompt"] = fmt.Sprintf("must not exceed %d bytes", s.cfg.MaxInputBytes)
	case !utf8.ValidString(req.Prompt):
		fields["prompt"] = "must be valid UTF-8"
	case req.Type == domain.HelperSuggestion && strings.TrimSpace(req.Prompt) == "":
		fields["prompt"] = "a suggestion needs your current draft"
	}
	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// helperInput lays out the task and the player's own draft (and test
// output) for the model. Player text is JSON-escaped inside its tags so it
// cannot close them; it is the player's own data only, never anyone else's.
func helperInput(c *domain.Challenge, t domain.HelperType, prompt, output string) string {
	var b strings.Builder
	b.WriteString("<task>\n")
	if c != nil {
		b.WriteString(challengeTask(c))
		if len(c.Constraints.RequiredElements) > 0 {
			fmt.Fprintf(&b, "\nThe output must include: %s.", strings.Join(c.Constraints.RequiredElements, ", "))
		}
		if c.Constraints.OutputFormat != "" {
			fmt.Fprintf(&b, "\nExpected output format: %s.", c.Constraints.OutputFormat)
		}
		for _, cr := range c.JudgeCriteria.Criteria {
			fmt.Fprintf(&b, "\nJudged on %s: %s", cr.Name, cr.Description)
		}
	} else {
		b.WriteString("(the round's task is not recorded)")
	}
	b.WriteString("\n</task>\n")
	if strings.TrimSpace(prompt) != "" {
		quoted, _ := json.Marshal(prompt)
		fmt.Fprintf(&b, "<player_prompt>\n%s\n</player_prompt>\n", quoted)
	}
	if t == domain.HelperReview && output != "" {
		quoted, _ := json.Marshal(output)
		fmt.Fprintf(&b, "<test_output>\n%s\n</test_output>\n", quoted)
	}
	return b.String()
}

// helperErrorCode names why a helper call failed.
func helperErrorCode(ctx context.Context, err error) string {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errRoundEnded):
		return HelperErrorRoundEnded
	case errors.Is(cause, errHelperTimeout):
		return HelperErrorTimeout
	}
	return strings.ToUpper(string(ai.KindOf(err)))
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n])
}
