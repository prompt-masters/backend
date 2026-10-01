package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// fakeClock is a Clock whose time only moves when a test says so. Timers that
// fall due run in the goroutine that advanced the clock.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	due   time.Time
	f     func()
	// done is set once the timer has fired or been stopped.
	done bool
}

func newFakeClock(now time.Time) *fakeClock {
	return &fakeClock{now: now}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, due: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	stopped := !t.done
	t.done = true
	return stopped
}

// Advance moves the clock forward by d, then fires every timer that has
// fallen due, earliest first.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.done && !t.due.After(c.now) && (next == nil || t.due.Before(next.due)) {
				next = t
			}
		}
		if next == nil {
			c.mu.Unlock()
			return
		}
		next.done = true
		c.mu.Unlock()
		next.f()
	}
}

// Jump moves the clock forward by d without firing any timer, as when a timer
// is late.
func (c *fakeClock) Jump(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// pending returns the due times of the timers that have neither fired nor
// been stopped.
func (c *fakeClock) pending() []time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	due := []time.Time{}
	for _, t := range c.timers {
		if !t.done {
			due = append(due, t.due)
		}
	}
	return due
}

type engineFixture struct {
	*gameServiceFixture
	host, guest uuid.UUID
	game        *domain.Game
}

// newEngineFixture seeds a waiting game for two players with rounds rounds of
// 90 seconds each.
func newEngineFixture(t *testing.T, rounds int) *engineFixture {
	t.Helper()
	f := &engineFixture{gameServiceFixture: newGameServiceFixture(), host: uuid.New(), guest: uuid.New()}
	t.Cleanup(f.engine.Close)
	f.game = f.store.seed(domain.GameStatusWaiting, "246800", settingsWith(func(s *domain.GameSettings) {
		s.Rounds = rounds
	}), f.host, f.guest)
	return f
}

func (f *engineFixture) progress() domain.GameProgress {
	return f.store.get(f.game.ID).Progress
}

// start begins round 1 and clears the events it published.
func (f *engineFixture) start(t *testing.T) domain.GameProgress {
	t.Helper()
	if _, err := f.engine.StartGame(context.Background(), f.host, f.game.ID, nil); err != nil {
		t.Fatalf("StartGame() error = %v", err)
	}
	f.clearEvents()
	return f.progress()
}

// submit runs a completed test for userID in the current round and submits it.
func (f *engineFixture) submit(userID uuid.UUID) error {
	test := f.addTest(userID, domain.PromptTestCompleted, "my prompt", "my output")
	_, _, err := f.engine.Submit(context.Background(), f.game.ID, userID, f.progress().RoundID, test.ID)
	return err
}

// addTest stores a prompt test for userID in the current round, numbered
// after their earlier ones, as the prompt test service would.
func (f *engineFixture) addTest(userID uuid.UUID, status domain.PromptTestStatus, prompt, output string) *domain.PromptTest {
	round := f.progress().RoundID
	test := &domain.PromptTest{
		ID: uuid.New(), GameID: f.game.ID, RoundID: round, PlayerID: userID,
		Prompt: prompt, Status: status, CreatedAt: f.clock.Now(),
	}
	if status == domain.PromptTestCompleted || output != "" {
		test.Output = &output
	}
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	for _, other := range f.store.promptTests {
		if other.RoundID == round && other.PlayerID == userID {
			test.TestNumber = max(test.TestNumber, other.TestNumber)
		}
	}
	test.TestNumber++
	c := *test
	f.store.promptTests[test.ID] = &c
	return test
}

// submissions returns the round's stored submissions.
func (f *engineFixture) submissions(roundID uuid.UUID) []domain.Submission {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	subs, _ := fakeSubmissionRepo{f.store}.ListByRound(context.Background(), roundID)
	return subs
}

func (f *engineFixture) clearEvents() {
	f.events.mu.Lock()
	defer f.events.mu.Unlock()
	f.events.published = nil
}

// judged runs the round in play to Results by letting its timer expire.
func (f *engineFixture) judged(t *testing.T) domain.GameProgress {
	t.Helper()
	p := f.progress()
	f.clock.Advance(p.RoundDeadline.Sub(f.clock.Now()))
	if err := f.engine.CompleteJudging(context.Background(), f.game.ID, p.RoundID); err != nil {
		t.Fatalf("CompleteJudging() error = %v", err)
	}
	if got := f.progress().Phase; got != domain.GamePhaseResults {
		t.Fatalf("phase = %s after judging, want Results", got)
	}
	f.clearEvents()
	return f.progress()
}

// states lists the phases announced by game_state events.
func (f *engineFixture) states() []domain.GamePhase {
	phases := []domain.GamePhase{}
	for _, e := range f.events.events() {
		if e.kind == "game_state" {
			phases = append(phases, e.progress.Phase)
		}
	}
	return phases
}

func (f *engineFixture) roundEnds() []string {
	reasons := []string{}
	for _, e := range f.events.events() {
		if e.kind == "round_end" {
			reasons = append(reasons, e.reason)
		}
	}
	return reasons
}

func TestGameEngineStartGame(t *testing.T) {
	ctx := context.Background()

	t.Run("host starts round 1 with a server deadline", func(t *testing.T) {
		f := newEngineFixture(t, 5)
		now := f.clock.Now()

		got, err := f.engine.StartGame(ctx, f.host, f.game.ID, nil)
		if err != nil {
			t.Fatalf("StartGame() error = %v", err)
		}
		p := f.progress()
		if p.Phase != domain.GamePhaseRoundActive || p.CurrentRound != 1 || p.TotalRounds != 5 || p.RoundID == uuid.Nil {
			t.Fatalf("stored progress = %+v, want round 1 of 5 active", p)
		}
		if !p.RoundStartedAt.Equal(now) || !p.RoundDeadline.Equal(now.Add(90*time.Second)) {
			t.Errorf("round timing = %v..%v, want %v..%v", p.RoundStartedAt, p.RoundDeadline, now, now.Add(90*time.Second))
		}
		if got.Progress != p || got.Status != domain.GameStatusInProgress {
			t.Errorf("returned game = %+v, want the stored state", got)
		}
		if due := f.clock.pending(); len(due) != 1 || !due[0].Equal(p.RoundDeadline) {
			t.Errorf("pending timers = %v, want one at the deadline %v", due, p.RoundDeadline)
		}
		if states := f.states(); !slices.Equal(states, []domain.GamePhase{domain.GamePhaseRoundActive}) {
			t.Errorf("published states = %v, want [RoundActive]", states)
		}
		if synced := f.live.synced(); len(synced) != 1 || synced[0].Status != domain.GameStatusInProgress {
			t.Errorf("synced %+v, want the in_progress game mirrored once", synced)
		}
	})

	t.Run("non-host cannot start", func(t *testing.T) {
		f := newEngineFixture(t, 5)
		if _, err := f.engine.StartGame(ctx, f.guest, f.game.ID, nil); !errors.Is(err, domain.ErrNotGameHost) {
			t.Fatalf("StartGame(guest) error = %v, want ErrNotGameHost", err)
		}
		if p := f.progress(); p.Phase != domain.GamePhaseLobby || p.RoundID != uuid.Nil {
			t.Errorf("progress = %+v after a refused start, want the lobby", p)
		}
		if n := len(f.events.events()) + len(f.clock.pending()) + len(f.live.synced()); n != 0 {
			t.Errorf("a refused start published, armed or synced %d things, want none", n)
		}
	})

	t.Run("a failing start check changes nothing", func(t *testing.T) {
		f := newEngineFixture(t, 5)
		_, err := f.engine.StartGame(ctx, f.host, f.game.ID, func(context.Context, repository.TxRepositories, *domain.Game) error {
			return domain.ErrNotEnoughPlayers
		})
		if !errors.Is(err, domain.ErrNotEnoughPlayers) {
			t.Fatalf("StartGame() error = %v, want the check's error", err)
		}
		if p := f.progress(); p.Phase != domain.GamePhaseLobby {
			t.Errorf("phase = %s, want Lobby", p.Phase)
		}
	})

	t.Run("a started or cancelled game cannot start again", func(t *testing.T) {
		f := newEngineFixture(t, 5)
		before := f.start(t)
		if _, err := f.engine.StartGame(ctx, f.host, f.game.ID, nil); !errors.Is(err, domain.ErrGameNotWaiting) {
			t.Fatalf("second StartGame() error = %v, want ErrGameNotWaiting", err)
		}
		if f.progress() != before {
			t.Errorf("progress changed by a refused start")
		}

		cancelled := newEngineFixture(t, 5)
		if err := cancelled.svc.Cancel(ctx, cancelled.host, cancelled.game.ID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := cancelled.engine.StartGame(ctx, cancelled.host, cancelled.game.ID, nil); !errors.Is(err, domain.ErrGameNotWaiting) {
			t.Fatalf("StartGame() on a cancelled game error = %v, want ErrGameNotWaiting", err)
		}
	})
}

func TestGameEngineTimerExpiresExactlyOnce(t *testing.T) {
	f := newEngineFixture(t, 5)
	round := f.start(t)

	f.clock.Advance(89*time.Second + 999*time.Millisecond)
	if p := f.progress(); p.Phase != domain.GamePhaseRoundActive {
		t.Fatalf("phase = %s a millisecond before the deadline, want RoundActive", p.Phase)
	}

	f.clock.Advance(time.Millisecond)
	if p := f.progress(); p.Phase != domain.GamePhaseJudging || p.RoundID != round.RoundID {
		t.Fatalf("progress = %+v at the deadline, want round %s judging", p, round.RoundID)
	}

	// Neither more time nor a duplicate timer event moves it again.
	f.clock.Advance(time.Hour)
	if err := f.engine.TimerExpired(context.Background(), f.game.ID, round.RoundID); err != nil {
		t.Fatalf("duplicate TimerExpired() error = %v, want a silent no-op", err)
	}
	if states := f.states(); !slices.Equal(states, []domain.GamePhase{domain.GamePhaseJudging}) {
		t.Errorf("published states = %v, want exactly one Judging", states)
	}
	if reasons := f.roundEnds(); !slices.Equal(reasons, []string{RoundEndTimeExpired}) {
		t.Errorf("round_end reasons = %v, want one time_expired", reasons)
	}
	if due := f.clock.pending(); len(due) != 0 {
		t.Errorf("pending timers = %v, want none once the round ended", due)
	}
}

func TestGameEngineAllSubmittedEndsTheRoundEarly(t *testing.T) {
	f := newEngineFixture(t, 5)
	round := f.start(t)
	f.clock.Advance(10 * time.Second)

	if err := f.submit(f.host); err != nil {
		t.Fatalf("Submit(host) error = %v", err)
	}
	if p := f.progress(); p.Phase != domain.GamePhaseRoundActive {
		t.Fatalf("phase = %s after one of two submissions, want RoundActive", p.Phase)
	}
	if err := f.submit(f.guest); err != nil {
		t.Fatalf("Submit(guest) error = %v", err)
	}
	if p := f.progress(); p.Phase != domain.GamePhaseJudging || p.RoundID != round.RoundID {
		t.Fatalf("progress = %+v after every submission, want Judging", p)
	}
	if due := f.clock.pending(); len(due) != 0 {
		t.Errorf("pending timers = %v, want the round timer stopped", due)
	}

	// The timer event arriving late anyway is a no-op, not an error.
	f.clock.Advance(time.Hour)
	if err := f.engine.TimerExpired(context.Background(), f.game.ID, round.RoundID); err != nil {
		t.Fatalf("late TimerExpired() error = %v", err)
	}

	kinds := f.events.kinds()
	want := []string{"player_submitted", "player_submitted", "round_end", "game_state"}
	if !slices.Equal(kinds, want) {
		t.Errorf("published %v, want %v", kinds, want)
	}
	if reasons := f.roundEnds(); !slices.Equal(reasons, []string{RoundEndAllSubmitted}) {
		t.Errorf("round_end reasons = %v, want one all_submitted", reasons)
	}
	if subs := f.submissions(round.RoundID); len(subs) != 2 || subs[0].Prompt != "my prompt" || subs[0].Output != "my output" || subs[0].Source != domain.SubmissionManual {
		t.Errorf("stored submissions = %+v", subs)
	}
	var counts []string
	for _, e := range f.events.events() {
		if e.kind == "player_submitted" {
			counts = append(counts, fmt.Sprintf("%d/%d", e.submitted, e.total))
		}
	}
	if !slices.Equal(counts, []string{"1/2", "2/2"}) {
		t.Errorf("player_submitted counts = %v, want 1/2 then 2/2", counts)
	}
}

func TestGameEngineRejectsSubmissions(t *testing.T) {
	ctx := context.Background()

	t.Run("outside an active round", func(t *testing.T) {
		f := newEngineFixture(t, 3)
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() in Lobby error = %v, want ErrRoundNotActive", err)
		}

		f.start(t)
		f.clock.Advance(90 * time.Second) // Judging
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() in Judging error = %v, want ErrRoundNotActive", err)
		}

		f.judged(t) // Results
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() in Results error = %v, want ErrRoundNotActive", err)
		}

		for range 2 {
			if err := f.engine.Advance(ctx, f.game.ID, f.progress().RoundID); err != nil {
				t.Fatal(err)
			}
			f.judged(t)
		}
		if err := f.engine.Advance(ctx, f.game.ID, f.progress().RoundID); err != nil {
			t.Fatal(err)
		}
		if p := f.progress(); p.Phase != domain.GamePhaseGameOver {
			t.Fatalf("phase = %s, want GameOver", p.Phase)
		}
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() in GameOver error = %v, want ErrRoundNotActive", err)
		}
	})

	t.Run("at or after the deadline, even before the timer fires", func(t *testing.T) {
		f := newEngineFixture(t, 3)
		f.start(t)
		f.clock.Jump(90 * time.Second)
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() at the deadline error = %v, want ErrRoundNotActive", err)
		}
		f.clock.Jump(time.Second)
		if err := f.submit(f.host); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("Submit() after the deadline error = %v, want ErrRoundNotActive", err)
		}
		if p := f.progress(); p.Phase != domain.GamePhaseRoundActive {
			t.Errorf("phase = %s, want the round left to its timer", p.Phase)
		}
	})

	t.Run("invalid submissions", func(t *testing.T) {
		f := newEngineFixture(t, 3)
		round := f.start(t)
		submit := func(user, roundID, testID uuid.UUID) (*domain.Submission, bool, error) {
			return f.engine.Submit(ctx, f.game.ID, user, roundID, testID)
		}

		failed := f.addTest(f.host, domain.PromptTestFailed, "p", "partial")
		guests := f.addTest(f.guest, domain.PromptTestCompleted, "guest's", "out")
		elsewhere := f.addTest(f.host, domain.PromptTestCompleted, "p", "out")
		f.store.mu.Lock()
		f.store.promptTests[elsewhere.ID].RoundID = uuid.New() // a test from another round
		f.store.mu.Unlock()
		valid := f.addTest(f.host, domain.PromptTestCompleted, "  héllo wörld  ", "the output")

		for name, tc := range map[string]struct {
			user, round, test uuid.UUID
			want              error
		}{
			"stranger":              {uuid.New(), round.RoundID, valid.ID, domain.ErrNotGamePlayer},
			"another round":         {f.host, uuid.New(), valid.ID, domain.ErrRoundNotActive},
			"failed test":           {f.host, round.RoundID, failed.ID, domain.ErrInvalidTest},
			"another player's test": {f.host, round.RoundID, guests.ID, domain.ErrInvalidTest},
			"test from elsewhere":   {f.host, round.RoundID, elsewhere.ID, domain.ErrInvalidTest},
			"unknown test":          {f.host, round.RoundID, uuid.New(), domain.ErrInvalidTest},
		} {
			if _, _, err := submit(tc.user, tc.round, tc.test); !errors.Is(err, tc.want) {
				t.Errorf("%s: Submit() error = %v, want %v", name, err, tc.want)
			}
		}

		sub, created, err := submit(f.host, round.RoundID, valid.ID)
		if err != nil || !created {
			t.Fatalf("Submit() = %v, %v; want a new submission", created, err)
		}
		// A snapshot of the test, with the counts scoring needs: the failed
		// test counts as an attempt, the other round's does not (2 in all),
		// and the length is code points of the trimmed prompt.
		if sub.Prompt != "  héllo wörld  " || sub.Output != "the output" || sub.PromptLength != 11 || sub.TestCount != 2 ||
			sub.TestID != valid.ID || sub.Source != domain.SubmissionManual || !sub.LockedAt.Equal(f.clock.Now()) || !sub.SubmittedAt.Equal(sub.LockedAt) {
			t.Errorf("submission = %+v", sub)
		}

		again, created, err := submit(f.host, round.RoundID, valid.ID)
		if err != nil || created || again.ID != sub.ID {
			t.Errorf("repeated Submit() = %+v, %v, %v; want the same submission, not created", again, created, err)
		}
		other := f.addTest(f.host, domain.PromptTestCompleted, "second", "out")
		if _, _, err := submit(f.host, round.RoundID, other.ID); !errors.Is(err, domain.ErrAlreadySubmitted) {
			t.Errorf("second Submit() error = %v, want ErrAlreadySubmitted", err)
		}
		if n := len(f.submissions(round.RoundID)); n != 1 {
			t.Errorf("stored %d submissions, want only the valid one", n)
		}
		if kinds := f.events.kinds(); !slices.Equal(kinds, []string{"player_submitted"}) {
			t.Errorf("published %v, want only the valid submission announced", kinds)
		}
	})
}

func TestGameEngineIgnoresStaleRoundEvents(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3)
	first := f.start(t)
	f.judged(t)
	if err := f.engine.Advance(ctx, f.game.ID, first.RoundID); err != nil {
		t.Fatal(err)
	}
	second := f.progress()
	if second.CurrentRound != 2 || second.RoundID == first.RoundID {
		t.Fatalf("progress = %+v, want a new round 2", second)
	}
	f.clearEvents()
	updates := f.store.updates

	for name, err := range map[string]error{
		"TimerExpired":    f.engine.TimerExpired(ctx, f.game.ID, first.RoundID),
		"CompleteJudging": f.engine.CompleteJudging(ctx, f.game.ID, first.RoundID),
		"Advance":         f.engine.Advance(ctx, f.game.ID, first.RoundID),
	} {
		if err != nil {
			t.Errorf("%s(round 1) error = %v, want a silent no-op", name, err)
		}
	}
	late := f.addTest(f.host, domain.PromptTestCompleted, "late", "out")
	if _, _, err := f.engine.Submit(ctx, f.game.ID, f.host, first.RoundID, late.ID); !errors.Is(err, domain.ErrRoundNotActive) {
		t.Errorf("Submit(round 1) error = %v, want ErrRoundNotActive", err)
	}

	if f.progress() != second {
		t.Errorf("progress = %+v, want round 2 untouched", f.progress())
	}
	if f.store.updates != updates || len(f.events.events()) != 0 {
		t.Errorf("stale events wrote %d updates and published %v, want nothing", f.store.updates-updates, f.events.kinds())
	}
}

func TestGameEngineRejectsInvalidTransitions(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3)
	round := f.start(t)

	// RoundActive: judging cannot complete and results cannot be left.
	for name, call := range map[string]func() error{
		"CompleteJudging": func() error { return f.engine.CompleteJudging(ctx, f.game.ID, round.RoundID) },
		"Advance":         func() error { return f.engine.Advance(ctx, f.game.ID, round.RoundID) },
	} {
		if err := call(); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("%s in RoundActive error = %v, want ErrInvalidTransition", name, err)
		}
	}
	// Judging: results cannot be left before judging completes.
	f.clock.Advance(90 * time.Second)
	f.clearEvents()
	judging := f.progress()
	if err := f.engine.Advance(ctx, f.game.ID, round.RoundID); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("Advance in Judging error = %v, want ErrInvalidTransition", err)
	}
	if f.progress() != judging || len(f.events.events()) != 0 {
		t.Errorf("a rejected transition changed the state or published %v", f.events.kinds())
	}

	// Lobby: nothing but startGame applies.
	lobby := newEngineFixture(t, 3)
	if err := lobby.engine.CompleteJudging(ctx, lobby.game.ID, uuid.Nil); !errors.Is(err, domain.ErrGameNotInProgress) {
		t.Errorf("CompleteJudging in Lobby error = %v, want ErrGameNotInProgress", err)
	}
	if err := lobby.engine.TimerExpired(ctx, lobby.game.ID, uuid.Nil); err != nil {
		t.Errorf("TimerExpired in Lobby error = %v, want a no-op", err)
	}
	if p := lobby.progress(); p.Phase != domain.GamePhaseLobby {
		t.Errorf("phase = %s, want Lobby", p.Phase)
	}
}

func TestGameEngineRoundProgression(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3)
	first := f.start(t)

	rounds := []domain.GameProgress{first}
	for n := 2; n <= 3; n++ {
		results := f.judged(t)
		f.clock.Advance(5 * time.Second)
		now := f.clock.Now()
		if err := f.engine.Advance(ctx, f.game.ID, results.RoundID); err != nil {
			t.Fatalf("Advance() after round %d error = %v", n-1, err)
		}
		p := f.progress()
		if p.Phase != domain.GamePhaseRoundActive || p.CurrentRound != n {
			t.Fatalf("progress = %+v, want round %d active", p, n)
		}
		if !p.RoundStartedAt.Equal(now) || !p.RoundDeadline.Equal(now.Add(90*time.Second)) {
			t.Errorf("round %d timing = %v..%v, want a fresh 90s from %v", n, p.RoundStartedAt, p.RoundDeadline, now)
		}
		if due := f.clock.pending(); len(due) != 1 || !due[0].Equal(p.RoundDeadline) {
			t.Errorf("pending timers = %v, want one at round %d's deadline", due, n)
		}
		rounds = append(rounds, p)
	}
	if rounds[0].RoundID == rounds[1].RoundID || rounds[1].RoundID == rounds[2].RoundID {
		t.Error("rounds share an ID")
	}

	final := f.judged(t)
	if err := f.engine.Advance(ctx, f.game.ID, final.RoundID); err != nil {
		t.Fatalf("Advance() after the final round error = %v", err)
	}
	g := f.store.get(f.game.ID)
	if g.Progress.Phase != domain.GamePhaseGameOver || g.Status != domain.GameStatusFinished {
		t.Fatalf("game = %s/%s, want GameOver and finished", g.Progress.Phase, g.Status)
	}
	if states := f.states(); !slices.Equal(states, []domain.GamePhase{domain.GamePhaseGameOver}) {
		t.Errorf("published states = %v, want [GameOver]", states)
	}
	if synced := f.live.synced(); synced[len(synced)-1].Status != domain.GameStatusFinished {
		t.Errorf("last synced status = %s, want finished", synced[len(synced)-1].Status)
	}
	if err := f.engine.Advance(ctx, f.game.ID, final.RoundID); !errors.Is(err, domain.ErrGameNotInProgress) {
		t.Errorf("Advance() after GameOver error = %v, want ErrGameNotInProgress", err)
	}
}

// TestGameEngineTimerRacesTheLastSubmission fires the deadline timer and the
// final submission at the same moment, many times over: exactly one of them
// may end the round. Run it with -race.
func TestGameEngineTimerRacesTheLastSubmission(t *testing.T) {
	const iterations = 1000
	outcomes := map[string]int{}

	for i := range iterations {
		f := newEngineFixture(t, 3)
		round := f.start(t)
		if err := f.submit(f.host); err != nil {
			t.Fatalf("iteration %d: Submit(host) error = %v", i, err)
		}
		f.clock.Jump(round.RoundDeadline.Sub(f.clock.Now()) - time.Millisecond)

		var (
			wg        sync.WaitGroup
			submitErr error
			start     = make(chan struct{})
		)
		wg.Go(func() {
			<-start
			f.clock.Advance(time.Millisecond) // reaches the deadline, fires the timer
		})
		wg.Go(func() {
			<-start
			submitErr = f.submit(f.guest)
		})
		close(start)
		wg.Wait()

		switch {
		case submitErr == nil:
			outcomes["submission won"]++
		case errors.Is(submitErr, domain.ErrRoundNotActive):
			outcomes["timer won"]++
		default:
			t.Fatalf("iteration %d: Submit(guest) error = %v", i, submitErr)
		}

		if p := f.progress(); p.Phase != domain.GamePhaseJudging || p.RoundID != round.RoundID {
			t.Fatalf("iteration %d: progress = %+v, want round %s judging", i, p, round.RoundID)
		}
		if states := f.states(); !slices.Equal(states, []domain.GamePhase{domain.GamePhaseJudging}) {
			t.Fatalf("iteration %d: published states %v, want exactly one Judging", i, states)
		}
		if ends := f.roundEnds(); len(ends) != 1 {
			t.Fatalf("iteration %d: round_end published %d times, want once", i, len(ends))
		}
		f.engine.Close()
	}
	t.Logf("outcomes over %d races: %v", iterations, outcomes)
}

func TestGameEngineRecover(t *testing.T) {
	ctx := context.Background()

	// seedRound stores a game whose round 2 has deadline, as a previous
	// server process would have left it.
	seedRound := func(f *engineFixture, status domain.GameStatus, deadline time.Time) *domain.Game {
		g := f.store.seed(status, "", validSettings, f.host, f.guest)
		f.store.mu.Lock()
		f.store.games[g.ID].Progress = domain.GameProgress{
			Phase:          domain.GamePhaseRoundActive,
			CurrentRound:   2,
			RoundID:        uuid.New(),
			RoundStartedAt: deadline.Add(-90 * time.Second),
			RoundDeadline:  deadline,
		}
		f.store.mu.Unlock()
		return f.store.get(g.ID)
	}

	f := newEngineFixture(t, 5)
	now := f.clock.Now()
	expired := seedRound(f, domain.GameStatusInProgress, now.Add(-time.Second))
	running := seedRound(f, domain.GameStatusInProgress, now.Add(40*time.Second))
	cancelled := seedRound(f, domain.GameStatusCancelled, now.Add(-time.Second))

	if err := f.engine.Recover(ctx); err != nil {
		t.Fatalf("Recover() error = %v", err)
	}

	if p := f.store.get(expired.ID).Progress; p.Phase != domain.GamePhaseJudging || p.RoundID != expired.Progress.RoundID {
		t.Errorf("expired round progress = %+v, want it judged", p)
	}
	if p := f.store.get(running.ID).Progress; p != running.Progress {
		t.Errorf("running round progress = %+v, want it untouched", p)
	}
	if p := f.store.get(cancelled.ID).Progress; p.Phase != domain.GamePhaseRoundActive {
		t.Errorf("cancelled game phase = %s, want it left alone", p.Phase)
	}
	// Re-armed for the 40s left, not a fresh 90s.
	if due := f.clock.pending(); len(due) != 1 || due[0].Sub(now) != 40*time.Second {
		t.Fatalf("pending timers = %v, want one due 40s from now", due)
	}

	// A second recovery, as by another instance, ends nothing twice.
	if err := f.engine.Recover(ctx); err != nil {
		t.Fatalf("second Recover() error = %v", err)
	}
	f.clock.Advance(40 * time.Second)
	if p := f.store.get(running.ID).Progress; p.Phase != domain.GamePhaseJudging {
		t.Errorf("running round phase = %s at its deadline, want Judging", p.Phase)
	}

	ends := map[uuid.UUID]int{}
	for _, e := range f.events.events() {
		if e.kind == "round_end" {
			ends[e.gameID]++
		}
	}
	if ends[expired.ID] != 1 || ends[running.ID] != 1 || ends[cancelled.ID] != 0 {
		t.Errorf("round_end counts = %v, want exactly one for each recovered round", ends)
	}
}

func TestGameEngineCloseStopsTimers(t *testing.T) {
	f := newEngineFixture(t, 3)
	f.start(t)
	f.engine.Close()

	f.clock.Advance(time.Hour)
	if p := f.progress(); p.Phase != domain.GamePhaseRoundActive {
		t.Errorf("phase = %s after Close, want the round left for Recover", p.Phase)
	}
}

func TestGameServiceCancelStopsTheRoundTimer(t *testing.T) {
	f := newEngineFixture(t, 3)
	f.start(t)
	if err := f.svc.Cancel(context.Background(), f.host, f.game.ID.String()); err != nil {
		t.Fatal(err)
	}
	if due := f.clock.pending(); len(due) != 0 {
		t.Errorf("pending timers = %v after cancel, want none", due)
	}
	f.clock.Advance(time.Hour)
	if g := f.store.get(f.game.ID); g.Status != domain.GameStatusCancelled || g.Progress.Phase != domain.GamePhaseRoundActive {
		t.Errorf("game = %s/%s, want it left cancelled", g.Status, g.Progress.Phase)
	}
}
