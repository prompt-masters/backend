package service

import (
	"context"
	"errors"
	"log"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

const (
	// engineOpTimeout bounds one timer-driven transition.
	engineOpTimeout = 10 * time.Second
	// timerRetryDelay spaces out retries of a timer-driven transition that
	// failed, for example while the database was briefly unreachable.
	timerRetryDelay = time.Second
)

// Round end reasons, as sent in round_end.
const (
	RoundEndAllSubmitted = "all_submitted"
	RoundEndTimeExpired  = "time_expired"
)

// StartCheck vets a waiting game before its first round starts. It runs inside
// the transaction that holds the game's row lock, so it must only use repos.
type StartCheck func(ctx context.Context, repos repository.TxRepositories, g *domain.Game) error

// GameEngine owns every change of a game's engine state (domain.GameProgress)
// and the round timer. The server is the only source of truth: round start
// and deadline come from the server clock, are stored in PostgreSQL, and are
// what every client is sent.
//
// Every transition follows the same steps:
//
//  1. take the game's in-process lock,
//  2. in one transaction: row-lock the game, decide the event from the
//     locked state, check it with domain.GameProgress.Next, and store the new
//     state with a compare-and-set (repository.GameRepository.UpdateProgress),
//  3. after the commit, and still under the in-process lock: arm or stop the
//     round timer, mirror the status into live state, and publish the new
//     state to the room.
//
// The in-process lock keeps a game's timers and broadcasts in commit order.
// The row lock and the compare-and-set make a transition happen at most once
// even when several server instances share the database: a duplicate or late
// event (the timer firing just after the last submission ended the round)
// finds the state already moved on and does nothing.
//
// Events that belong to a round carry its ID, and an event for any other round
// is ignored.
type GameEngine struct {
	tx     repository.Transactor
	clock  Clock
	events GameEventPublisher
	live   LiveStateSyncer
	logger *log.Logger
	newID  func() uuid.UUID
	opts   EngineOptions
	// background is cancelled by Close; judging runs under it.
	background context.Context
	stop       context.CancelFunc

	// mu guards games, closed, judge and every engineGame's timer fields.
	mu       sync.Mutex
	games    map[uuid.UUID]*engineGame
	closed   bool
	judge    RoundJudger
	inflight sync.WaitGroup
	// roundEnd is told when a round stops being active.
	roundEnd []func(gameID, roundID uuid.UUID)
}

// RoundJudger scores a round once it has entered Judging. The engine calls it
// on its own goroutine after the transition has been committed and the game's
// lock released, so AI latency never holds up transitions or the round timer.
// ctx is cancelled when the engine closes.
type RoundJudger interface {
	JudgeRound(ctx context.Context, gameID, roundID uuid.UUID)
}

// engineGame is what the engine keeps in memory for one game.
type engineGame struct {
	// transition serializes the game's transitions in this process, from the
	// transaction through the broadcast.
	transition sync.Mutex

	// The armed timer: it calls fire for roundID at deadline. It is the
	// round timer (timerExpired) or the results timer (Advance).
	timer    Timer
	roundID  uuid.UUID
	deadline time.Time
}

// timerFire is what an armed timer does when it goes off.
type timerFire func(ctx context.Context, gameID, roundID uuid.UUID) error

// EngineOptions configures the engine.
type EngineOptions struct {
	// Random picks each round's challenge; nil uses MathRandom.
	Random Random
	// ResultsDuration is how long a round's results are shown before the
	// game moves on by itself; 0 leaves that to Advance's callers.
	ResultsDuration time.Duration
	// HelperBalance is the helper points every player gets when the game
	// starts; 0 gives none.
	HelperBalance int
}

func NewGameEngine(tx repository.Transactor, clock Clock, events GameEventPublisher, live LiveStateSyncer, logger *log.Logger, opts EngineOptions) *GameEngine {
	if opts.Random == nil {
		opts.Random = MathRandom{}
	}
	background, stop := context.WithCancel(context.Background())
	return &GameEngine{
		tx:         tx,
		clock:      clock,
		events:     events,
		live:       live,
		logger:     logger,
		newID:      uuid.New,
		opts:       opts,
		background: background,
		stop:       stop,
		games:      map[uuid.UUID]*engineGame{},
	}
}

// OnRoundEnd registers fn to be told, right after the commit, whenever a
// round stops being active (all submitted or time up). fn runs under the
// game's transition lock, so it must only signal, never block. Register
// during start-up.
func (e *GameEngine) OnRoundEnd(fn func(gameID, roundID uuid.UUID)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.roundEnd = append(e.roundEnd, fn)
}

// SetJudge attaches the round judge. It exists because the judge needs the
// engine to complete judging; call it during start-up, before Recover.
func (e *GameEngine) SetJudge(judge RoundJudger) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.judge = judge
}

// step is what a decision asks of apply: the event to fire, if any, and a
// notice to publish after the commit, ahead of the new state.
type step struct {
	event  domain.GameEvent
	notify func(ctx context.Context, game *domain.Game)
}

// StartGame starts the first round of a waiting game. Only the host may start
// it, and check must pass.
func (e *GameEngine) StartGame(ctx context.Context, userID, gameID uuid.UUID, check StartCheck) (*domain.Game, error) {
	return e.apply(ctx, gameID, func(repos repository.TxRepositories, g *domain.Game, _ time.Time) (step, error) {
		if !g.IsHost(userID) {
			return step{}, domain.ErrNotGameHost
		}
		if g.Status != domain.GameStatusWaiting {
			return step{}, domain.ErrGameNotWaiting
		}
		if check != nil {
			if err := check(ctx, repos, g); err != nil {
				return step{}, err
			}
		}
		return step{event: domain.GameEventStartGame}, nil
	})
}

// Submit makes the completed prompt test testID userID's final submission for
// roundID, and ends the round once every player has submitted. It reports
// false for a repeat of the player's existing submission, which it returns
// unchanged. It is refused with domain.ErrRoundNotActive outside the round or
// at its deadline, domain.ErrAlreadySubmitted when the player submitted
// another test, and domain.ErrInvalidTest when testID is not one of the
// player's completed tests in the round.
func (e *GameEngine) Submit(ctx context.Context, gameID, userID, roundID, testID uuid.UUID) (*domain.Submission, bool, error) {
	var (
		sub     *domain.Submission
		created bool
	)
	_, err := e.apply(ctx, gameID, func(repos repository.TxRepositories, g *domain.Game, now time.Time) (step, error) {
		if !g.HasPlayer(userID) {
			return step{}, domain.ErrNotGamePlayer
		}
		existing, err := repos.Submissions.GetForPlayer(ctx, roundID, userID)
		switch {
		case err == nil && existing.TestID == testID:
			sub = existing // a retry of the same request
			return step{}, nil
		case err != nil && !errors.Is(err, domain.ErrNotFound):
			return step{}, err
		}
		p := g.Progress
		if g.Status != domain.GameStatusInProgress || p.RoundID != roundID || !p.AcceptsSubmissions(now) {
			return step{}, domain.ErrRoundNotActive
		}
		if existing != nil {
			return step{}, domain.ErrAlreadySubmitted
		}
		test, err := repos.PromptTests.Get(ctx, testID)
		if errors.Is(err, domain.ErrNotFound) {
			return step{}, domain.ErrInvalidTest
		}
		if err != nil {
			return step{}, err
		}
		if test.PlayerID != userID || test.RoundID != roundID || test.Status != domain.PromptTestCompleted {
			return step{}, domain.ErrInvalidTest
		}

		sub, created, err = submitTest(ctx, repos, test, domain.SubmissionManual, now)
		if err != nil {
			return step{}, err
		}
		if !created {
			return step{}, domain.ErrAlreadySubmitted
		}
		submitted, err := repos.Submissions.Count(ctx, roundID)
		if err != nil {
			return step{}, err
		}
		total := len(g.Players)
		s := step{notify: func(ctx context.Context, game *domain.Game) {
			e.events.PlayerSubmitted(ctx, game, userID, submitted, total)
		}}
		// Players cannot leave a game in progress, so every player is active.
		if submitted >= total {
			s.event = domain.GameEventAllSubmitted
		}
		return s, nil
	})
	if err != nil {
		return nil, false, err
	}
	return sub, created, nil
}

// submitTest writes a submission snapshotting test. It reports false when the
// player already has a submission for the round, which then stays.
func submitTest(ctx context.Context, repos repository.TxRepositories, test *domain.PromptTest, source domain.SubmissionSource, now time.Time) (*domain.Submission, bool, error) {
	count, err := repos.PromptTests.Count(ctx, test.RoundID, test.PlayerID)
	if err != nil {
		return nil, false, err
	}
	sub := &domain.Submission{
		GameID: test.GameID, RoundID: test.RoundID, PlayerID: test.PlayerID, TestID: test.ID,
		Prompt: test.Prompt, PromptLength: domain.PromptLength(test.Prompt), TestCount: count,
		Source: source, SubmittedAt: now, LockedAt: now,
	}
	if test.Output != nil {
		sub.Output = *test.Output
	}
	created, err := repos.Submissions.Create(ctx, sub)
	return sub, created, err
}

// TimerExpired ends roundID once its deadline has passed, first submitting
// each player's last completed test for them if they had not submitted. It
// does nothing when that round is no longer in play, which is how a timer
// that lost the race to the last submission, or one from an earlier round,
// is ignored.
func (e *GameEngine) TimerExpired(ctx context.Context, gameID, roundID uuid.UUID) error {
	_, err := e.apply(ctx, gameID, func(repos repository.TxRepositories, g *domain.Game, now time.Time) (step, error) {
		if g.Status != domain.GameStatusInProgress || g.Progress.Phase != domain.GamePhaseRoundActive || g.Progress.RoundID != roundID {
			return step{}, nil
		}
		if !now.Before(g.Progress.RoundDeadline) {
			// In the transaction that ends the round, so every auto-submission
			// is committed before judging can read submissions.
			if err := e.autoSubmit(ctx, repos, g, now); err != nil {
				return step{}, err
			}
		}
		return step{event: domain.GameEventTimerExpired}, nil
	})
	return err
}

// autoSubmit submits the last completed test of every player who has not
// submitted. A player with no completed test gets no submission; judging
// scores them as no_submission. A manual submission that won the race keeps
// its place: the insert does nothing on conflict.
func (e *GameEngine) autoSubmit(ctx context.Context, repos repository.TxRepositories, g *domain.Game, now time.Time) error {
	roundID := g.Progress.RoundID
	var submitted, without int
	for _, p := range g.Players {
		_, err := repos.Submissions.GetForPlayer(ctx, roundID, p.UserID)
		if err == nil {
			continue
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		test, err := repos.PromptTests.LastCompleted(ctx, roundID, p.UserID)
		if errors.Is(err, domain.ErrNotFound) {
			without++
			continue
		}
		if err != nil {
			return err
		}
		if _, created, err := submitTest(ctx, repos, test, domain.SubmissionAuto, now); err != nil {
			return err
		} else if created {
			submitted++
		}
	}
	if submitted+without > 0 {
		e.logger.Printf("game_engine op=auto_submit game_id=%s round_id=%s submitted=%d without_completed_test=%d", g.ID, roundID, submitted, without)
	}
	return nil
}

// CompleteJudging moves roundID from Judging to Results. It is meant for the
// judging service; an event for another round is ignored.
func (e *GameEngine) CompleteJudging(ctx context.Context, gameID, roundID uuid.UUID) error {
	return e.roundEvent(ctx, gameID, roundID, func(domain.GameProgress) domain.GameEvent {
		return domain.GameEventJudgeCompleted
	})
}

// Advance leaves the Results of roundID: it starts the next round while rounds
// remain and ends the game after the last. An event for another round is
// ignored, so repeating it is harmless.
func (e *GameEngine) Advance(ctx context.Context, gameID, roundID uuid.UUID) error {
	return e.roundEvent(ctx, gameID, roundID, func(p domain.GameProgress) domain.GameEvent {
		if p.CurrentRound < p.TotalRounds {
			return domain.GameEventNextRound
		}
		return domain.GameEventFinalRound
	})
}

func (e *GameEngine) roundEvent(ctx context.Context, gameID, roundID uuid.UUID, event func(domain.GameProgress) domain.GameEvent) error {
	_, err := e.apply(ctx, gameID, func(_ repository.TxRepositories, g *domain.Game, _ time.Time) (step, error) {
		if g.Status != domain.GameStatusInProgress {
			return step{}, domain.ErrGameNotInProgress
		}
		if g.Progress.RoundID != roundID {
			return step{}, nil
		}
		return step{event: event(g.Progress)}, nil
	})
	return err
}

// Recover resumes the rounds that were in play when the server stopped: a
// round whose deadline has passed is ended, any other has its timer armed for
// the time it has left, and a round left in Judging is judged again. Other
// instances may recover the same rounds; the compare-and-set lets only one
// of them end each.
func (e *GameEngine) Recover(ctx context.Context) error {
	var games, judging, results []*domain.Game
	err := e.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		if games, err = repos.Games.ListByPhase(ctx, domain.GamePhaseRoundActive); err != nil {
			return err
		}
		if judging, err = repos.Games.ListByPhase(ctx, domain.GamePhaseJudging); err != nil {
			return err
		}
		results, err = repos.Games.ListByPhase(ctx, domain.GamePhaseResults)
		return err
	})
	if err != nil {
		return err
	}
	for _, g := range judging {
		if g.Status == domain.GameStatusInProgress {
			e.startJudging(g.ID, g.Progress.RoundID)
		}
	}
	if e.opts.ResultsDuration > 0 {
		// When results began is not stored; they are shown in full again.
		for _, g := range results {
			if g.Status == domain.GameStatusInProgress {
				e.armTimer(g.ID, g.Progress.RoundID, e.clock.Now().Add(e.opts.ResultsDuration), e.clock.Now(), e.Advance)
			}
		}
	}

	var (
		errs            []error
		rearmed, closed int
	)
	for _, g := range games {
		if g.Status != domain.GameStatusInProgress {
			continue
		}
		p := g.Progress
		if now := e.clock.Now(); now.Before(p.RoundDeadline) {
			eg := e.game(g.ID)
			eg.transition.Lock()
			e.armTimer(g.ID, p.RoundID, p.RoundDeadline, now, e.TimerExpired)
			eg.transition.Unlock()
			rearmed++
			continue
		}
		if err := e.TimerExpired(ctx, g.ID, p.RoundID); err != nil {
			errs = append(errs, err)
			continue
		}
		closed++
	}
	e.logger.Printf("game_engine op=recover rounds_rearmed=%d rounds_expired=%d failed=%d", rearmed, closed, len(errs))
	return errors.Join(errs...)
}

// Forget stops the game's timer and drops what the engine keeps in memory for
// it. GameService calls it once a game is cancelled.
func (e *GameEngine) Forget(gameID uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g, ok := e.games[gameID]; ok {
		g.stopTimer()
		delete(e.games, gameID)
	}
}

// Close stops every timer, cancels judging in progress, and waits for both.
// Rounds left in play are picked up by Recover on the next start.
func (e *GameEngine) Close() {
	e.mu.Lock()
	e.closed = true
	for _, g := range e.games {
		g.stopTimer()
	}
	e.mu.Unlock()
	e.stop()
	e.inflight.Wait()
}

// Rejudge judges roundID again, as after a failed run. The judging service
// checks it may; a round that is held or done is left alone by the claim.
func (e *GameEngine) Rejudge(gameID, roundID uuid.UUID) {
	e.startJudging(gameID, roundID)
}

// pickChallenge chooses the challenge for the game's next round: one for its
// category and difficulty, avoiding those already played while any remain.
func (e *GameEngine) pickChallenge(ctx context.Context, repos repository.TxRepositories, g *domain.Game) (uuid.UUID, error) {
	used, err := repos.Games.ListChallengeIDs(ctx, g.ID)
	if err != nil {
		return uuid.Nil, err
	}
	c, err := NewChallengeFactory(repos.Challenges, e.opts.Random).Create(ctx, ChallengeRequest{
		Category: g.Settings.Category, Difficulty: g.Settings.Difficulty, UsedChallengeIDs: used,
	})
	if err != nil {
		return uuid.Nil, err
	}
	return c.ID, nil
}

// startJudging hands roundID to the judge on its own goroutine.
func (e *GameEngine) startJudging(gameID, roundID uuid.UUID) {
	e.mu.Lock()
	judge := e.judge
	if e.closed || judge == nil {
		e.mu.Unlock()
		return
	}
	e.inflight.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.inflight.Done()
		judge.JudgeRound(e.background, gameID, roundID)
	}()
}

// apply runs one transition of gameID; see GameEngine for the steps. decide
// sees the row-locked game and the server time; the step it returns may carry
// no event, in which case its writes are committed without a transition. The
// returned game is the state after the commit.
func (e *GameEngine) apply(
	ctx context.Context,
	gameID uuid.UUID,
	decide func(repos repository.TxRepositories, g *domain.Game, now time.Time) (step, error),
) (*domain.Game, error) {
	eg := e.game(gameID)
	eg.transition.Lock()
	defer eg.transition.Unlock()

	now := e.clock.Now()
	var (
		game *domain.Game
		from domain.GameProgress
		s    step
	)
	err := e.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		locked, err := repos.Games.LockByID(ctx, gameID)
		if err != nil {
			return err
		}
		if s, err = decide(repos, locked, now); err != nil {
			return err
		}
		game, from = locked, locked.Progress
		if s.event == "" {
			return nil
		}
		plan := domain.RoundPlan{ID: e.newID(), Length: roundLength(locked.Settings)}
		if s.event == domain.GameEventStartGame || s.event == domain.GameEventNextRound {
			if plan.ChallengeID, err = e.pickChallenge(ctx, repos, locked); err != nil {
				return err
			}
		}
		to, err := from.Next(s.event, now, plan)
		if err != nil {
			return err
		}
		if err := repos.Games.UpdateProgress(ctx, gameID, from, to); err != nil {
			return err
		}
		if s.event == domain.GameEventStartGame && e.opts.HelperBalance > 0 {
			for _, p := range locked.Players {
				if err := repos.Helpers.EnsureBalance(ctx, gameID, p.UserID, e.opts.HelperBalance, now); err != nil {
					return err
				}
			}
		}
		if to.RoundID != from.RoundID {
			err := repos.Games.CreateRound(ctx, domain.Round{
				ID: to.RoundID, GameID: gameID, Number: to.CurrentRound, ChallengeID: to.ChallengeID,
				StartedAt: to.RoundStartedAt, Deadline: to.RoundDeadline,
			})
			if err != nil {
				return err
			}
		}
		game, err = repos.Games.GetByID(ctx, gameID)
		return err
	})
	if err != nil {
		return nil, err
	}

	if s.notify != nil {
		s.notify(ctx, game)
	}
	if s.event != "" {
		e.committed(ctx, from, game, s.event, now)
	}
	return game, nil
}

// committed carries out what follows a stored transition. The caller holds
// the game's transition lock.
func (e *GameEngine) committed(ctx context.Context, from domain.GameProgress, game *domain.Game, event domain.GameEvent, now time.Time) {
	to := game.Progress
	e.logger.Printf("game_engine op=transition game_id=%s event=%s from=%s to=%s round=%d round_id=%s",
		game.ID, event, from.Phase, to.Phase, to.CurrentRound, to.RoundID)

	switch {
	case to.Phase == domain.GamePhaseRoundActive:
		e.armTimer(game.ID, to.RoundID, to.RoundDeadline, now, e.TimerExpired)
	case to.Phase == domain.GamePhaseResults && e.opts.ResultsDuration > 0:
		e.armTimer(game.ID, to.RoundID, now.Add(e.opts.ResultsDuration), now, e.Advance)
	case to.Phase == domain.GamePhaseGameOver:
		e.Forget(game.ID)
	case from.Phase == domain.GamePhaseRoundActive:
		e.stopTimer(game.ID)
	}

	if from.Phase == domain.GamePhaseRoundActive && to.Phase != domain.GamePhaseRoundActive {
		e.mu.Lock()
		listeners := slices.Clone(e.roundEnd)
		e.mu.Unlock()
		for _, fn := range listeners {
			fn(game.ID, from.RoundID)
		}
	}
	if from.Phase.Status() != to.Phase.Status() {
		e.live.SyncGame(ctx, game)
	}
	if from.Phase == domain.GamePhaseRoundActive && to.Phase == domain.GamePhaseJudging {
		reason := RoundEndTimeExpired
		if event == domain.GameEventAllSubmitted {
			reason = RoundEndAllSubmitted
		}
		e.events.RoundEnded(ctx, game, reason)
	}
	e.events.GameUpdated(ctx, game)
	if to.Phase == domain.GamePhaseJudging {
		// Published first, so the room sees Judging before any result.
		e.startJudging(game.ID, to.RoundID)
	}
}

// game returns the engine's in-memory record of gameID, creating it on first
// use. Forget may drop a record while a caller still holds its lock, so a
// second record can briefly exist for a game that has just ended or been
// cancelled; the database's row lock and compare-and-set still keep its
// transitions correct.
func (e *GameEngine) game(gameID uuid.UUID) *engineGame {
	e.mu.Lock()
	defer e.mu.Unlock()
	g, ok := e.games[gameID]
	if !ok {
		g = &engineGame{}
		e.games[gameID] = g
	}
	return g
}

// armTimer (re)arms the game's timer to call fire for roundID at deadline,
// replacing any timer already armed.
func (e *GameEngine) armTimer(gameID, roundID uuid.UUID, deadline, now time.Time, fire timerFire) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return
	}
	g, ok := e.games[gameID]
	if !ok {
		g = &engineGame{}
		e.games[gameID] = g
	}
	g.stopTimer()
	g.roundID, g.deadline = roundID, deadline
	g.timer = e.clock.AfterFunc(max(deadline.Sub(now), 0), func() { e.onTimer(gameID, roundID, fire) })
}

func (e *GameEngine) stopTimer(gameID uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g, ok := e.games[gameID]; ok {
		g.stopTimer()
	}
}

// onTimer calls fire for roundID. A failure is retried while the timer is
// still the game's, because nothing else would move the game on.
func (e *GameEngine) onTimer(gameID, roundID uuid.UUID, fire timerFire) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.inflight.Add(1)
	e.mu.Unlock()
	defer e.inflight.Done()

	ctx, cancel := context.WithTimeout(context.Background(), engineOpTimeout)
	defer cancel()
	err := fire(ctx, gameID, roundID)
	if err == nil {
		return
	}
	e.logger.Printf("game_engine op=timer game_id=%s round_id=%s error=%q", gameID, roundID, err)

	e.mu.Lock()
	defer e.mu.Unlock()
	g, ok := e.games[gameID]
	if e.closed || !ok || g.roundID != roundID {
		return
	}
	// A timer that fired before the stored deadline (the wall clock moved)
	// waits out the rest; anything else is retried shortly.
	delay := g.deadline.Sub(e.clock.Now())
	if delay <= 0 {
		delay = timerRetryDelay
	}
	g.timer = e.clock.AfterFunc(delay, func() { e.onTimer(gameID, roundID, fire) })
}

// stopTimer is called with the engine's mu held.
func (g *engineGame) stopTimer() {
	if g.timer != nil {
		g.timer.Stop()
		g.timer = nil
	}
	g.roundID, g.deadline = uuid.Nil, time.Time{}
}

func roundLength(s domain.GameSettings) time.Duration {
	return time.Duration(s.TimePerRoundSeconds) * time.Second
}
