package service

import (
	"context"
	"errors"
	"log"
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
	// background is cancelled by Close; judging runs under it.
	background context.Context
	stop       context.CancelFunc

	// mu guards games, closed, judge and every engineGame's timer fields.
	mu       sync.Mutex
	games    map[uuid.UUID]*engineGame
	closed   bool
	judge    RoundJudger
	inflight sync.WaitGroup
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

	// The armed round timer, which fires timerExpired for roundID.
	timer    Timer
	roundID  uuid.UUID
	deadline time.Time
}

func NewGameEngine(tx repository.Transactor, clock Clock, events GameEventPublisher, live LiveStateSyncer, logger *log.Logger) *GameEngine {
	background, stop := context.WithCancel(context.Background())
	return &GameEngine{
		tx:         tx,
		clock:      clock,
		events:     events,
		live:       live,
		logger:     logger,
		newID:      uuid.New,
		background: background,
		stop:       stop,
		games:      map[uuid.UUID]*engineGame{},
	}
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

// Submit records userID's prompt for the round in play and ends the round
// once every player has submitted. It is refused with domain.ErrRoundNotActive
// outside an active round, at or after its deadline, or when roundID names
// another round; uuid.Nil means the current round.
func (e *GameEngine) Submit(ctx context.Context, gameID, userID, roundID uuid.UUID, prompt string) error {
	if msg := validateDraft(prompt); msg != "" {
		return &ValidationError{Fields: map[string]string{"prompt": msg}}
	}
	_, err := e.apply(ctx, gameID, func(repos repository.TxRepositories, g *domain.Game, now time.Time) (step, error) {
		if !g.HasPlayer(userID) {
			return step{}, domain.ErrNotGamePlayer
		}
		p := g.Progress
		if g.Status != domain.GameStatusInProgress || !p.AcceptsSubmissions(now) ||
			(roundID != uuid.Nil && roundID != p.RoundID) {
			return step{}, domain.ErrRoundNotActive
		}

		err := repos.Submissions.Create(ctx, domain.Submission{
			RoundID:     p.RoundID,
			GameID:      g.ID,
			RoundNumber: p.CurrentRound,
			UserID:      userID,
			Prompt:      prompt,
			SubmittedAt: now,
		})
		if err != nil {
			return step{}, err
		}
		submitted, err := repos.Submissions.CountByRound(ctx, p.RoundID)
		if err != nil {
			return step{}, err
		}

		s := step{notify: func(ctx context.Context, game *domain.Game) {
			e.events.PlayerSubmitted(ctx, game, userID, now)
		}}
		// Players cannot leave a game in progress, so every player is active.
		if submitted >= len(g.Players) {
			s.event = domain.GameEventAllSubmitted
		}
		return s, nil
	})
	return err
}

// TimerExpired ends roundID once its deadline has passed. It does nothing when
// that round is no longer in play, which is how a timer that lost the race to
// the last submission, or one from an earlier round, is ignored.
func (e *GameEngine) TimerExpired(ctx context.Context, gameID, roundID uuid.UUID) error {
	_, err := e.apply(ctx, gameID, func(_ repository.TxRepositories, g *domain.Game, _ time.Time) (step, error) {
		if g.Status != domain.GameStatusInProgress || g.Progress.Phase != domain.GamePhaseRoundActive || g.Progress.RoundID != roundID {
			return step{}, nil
		}
		return step{event: domain.GameEventTimerExpired}, nil
	})
	return err
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
	var games, judging []*domain.Game
	err := e.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		if games, err = repos.Games.ListByPhase(ctx, domain.GamePhaseRoundActive); err != nil {
			return err
		}
		judging, err = repos.Games.ListByPhase(ctx, domain.GamePhaseJudging)
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
			e.armTimer(g.ID, p.RoundID, p.RoundDeadline, now)
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
		to, err := from.Next(s.event, now, domain.RoundPlan{ID: e.newID(), Length: roundLength(locked.Settings)})
		if err != nil {
			return err
		}
		if err := repos.Games.UpdateProgress(ctx, gameID, from, to); err != nil {
			return err
		}
		if to.RoundID != from.RoundID {
			err := repos.Games.CreateRound(ctx, domain.Round{
				ID: to.RoundID, GameID: gameID, Number: to.CurrentRound,
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
		e.armTimer(game.ID, to.RoundID, to.RoundDeadline, now)
	case to.Phase == domain.GamePhaseGameOver:
		e.Forget(game.ID)
	case from.Phase == domain.GamePhaseRoundActive:
		e.stopTimer(game.ID)
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

// armTimer (re)arms the game's round timer to fire timerExpired for roundID at
// deadline, replacing any timer already armed.
func (e *GameEngine) armTimer(gameID, roundID uuid.UUID, deadline, now time.Time) {
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
	g.timer = e.clock.AfterFunc(max(deadline.Sub(now), 0), func() { e.onTimer(gameID, roundID) })
}

func (e *GameEngine) stopTimer(gameID uuid.UUID) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if g, ok := e.games[gameID]; ok {
		g.stopTimer()
	}
}

// onTimer fires timerExpired for roundID. A failure is retried until the
// round has ended, because nothing else would end it.
func (e *GameEngine) onTimer(gameID, roundID uuid.UUID) {
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
	err := e.TimerExpired(ctx, gameID, roundID)
	if err == nil {
		return
	}
	e.logger.Printf("game_engine op=timer_expired game_id=%s round_id=%s error=%q", gameID, roundID, err)

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
	g.timer = e.clock.AfterFunc(delay, func() { e.onTimer(gameID, roundID) })
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
