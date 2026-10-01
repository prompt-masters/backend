package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/ai/aitest"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/ws"
)

// fakePromptTestRepo follows the PostgreSQL repository's rules.
type fakePromptTestRepo struct{ s *fakeGameStore }

func (r fakePromptTestRepo) NextNumber(_ context.Context, roundID, playerID uuid.UUID) (int, error) {
	n := 0
	for _, t := range r.s.promptTests {
		if t.RoundID == roundID && t.PlayerID == playerID {
			n = max(n, t.TestNumber)
		}
	}
	return n + 1, nil
}

func (r fakePromptTestRepo) CountUnfinished(_ context.Context, roundID, playerID uuid.UUID, since time.Time) (int, error) {
	n := 0
	for _, t := range r.s.promptTests {
		if t.RoundID == roundID && t.PlayerID == playerID && !t.Status.Terminal() && !t.CreatedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (r fakePromptTestRepo) Create(_ context.Context, t *domain.PromptTest) error {
	for _, other := range r.s.promptTests {
		if other.RoundID == t.RoundID && other.PlayerID == t.PlayerID && other.TestNumber == t.TestNumber {
			return errors.New("duplicate test number")
		}
	}
	t.ID = uuid.New()
	c := *t
	r.s.promptTests[t.ID] = &c
	return nil
}

func (r fakePromptTestRepo) MarkStreaming(_ context.Context, id uuid.UUID, startedAt time.Time) error {
	t, ok := r.s.promptTests[id]
	if !ok || t.Status != domain.PromptTestPending {
		return domain.ErrNotFound
	}
	t.Status, t.StartedAt = domain.PromptTestStreaming, &startedAt
	return nil
}

func (r fakePromptTestRepo) Finish(_ context.Context, id uuid.UUID, res domain.PromptTestResult) error {
	t, ok := r.s.promptTests[id]
	if !ok || t.Status.Terminal() {
		return domain.ErrNotFound
	}
	completed := res.CompletedAt
	t.Status, t.Output, t.ErrorCode, t.Provider, t.Model = res.Status, res.Output, res.ErrorCode, res.Provider, res.Model
	t.InputTokens, t.OutputTokens, t.CostUSD, t.LatencyMs, t.CompletedAt = res.InputTokens, res.OutputTokens, res.CostUSD, res.LatencyMs, &completed
	return nil
}

func (r fakePromptTestRepo) ListByRoundAndPlayer(_ context.Context, roundID, playerID uuid.UUID) ([]domain.PromptTest, error) {
	out := []domain.PromptTest{}
	for _, t := range r.s.promptTests {
		if t.RoundID == roundID && t.PlayerID == playerID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TestNumber < out[j].TestNumber })
	return out, nil
}

func (r fakePromptTestRepo) FailStale(_ context.Context, before, now time.Time) (int64, error) {
	var n int64
	for _, t := range r.s.promptTests {
		if !t.Status.Terminal() && t.CreatedAt.Before(before) {
			t.Status, t.ErrorCode, t.CompletedAt = domain.PromptTestFailed, TestErrorInterrupted, &now
			n++
		}
	}
	return n, nil
}

func (r fakePromptTestRepo) Get(_ context.Context, id uuid.UUID) (*domain.PromptTest, error) {
	t, ok := r.s.promptTests[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	c := *t
	return &c, nil
}

func (r fakePromptTestRepo) Count(_ context.Context, roundID, playerID uuid.UUID) (int, error) {
	n := 0
	for _, t := range r.s.promptTests {
		if t.RoundID == roundID && t.PlayerID == playerID {
			n++
		}
	}
	return n, nil
}

func (r fakePromptTestRepo) LastCompleted(_ context.Context, roundID, playerID uuid.UUID) (*domain.PromptTest, error) {
	var last *domain.PromptTest
	for _, t := range r.s.promptTests {
		if t.RoundID == roundID && t.PlayerID == playerID && t.Status == domain.PromptTestCompleted && (last == nil || t.TestNumber > last.TestNumber) {
			last = t
		}
	}
	if last == nil {
		return nil, domain.ErrNotFound
	}
	c := *last
	return &c, nil
}

func (s *fakeGameStore) promptTest(id uuid.UUID) domain.PromptTest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.promptTests[id]
}

// sent is one event delivered by the hub.
type sent struct {
	userID uuid.UUID
	env    ws.Envelope
}

// targetHub records who each event was sent to.
type targetHub struct {
	mu         sync.Mutex
	sent       []sent
	broadcasts int
}

func (h *targetHub) Broadcast(uuid.UUID, ws.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcasts++
}

func (h *targetHub) BroadcastExcept(uuid.UUID, uuid.UUID, ws.Message) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.broadcasts++
}

func (h *targetHub) SendToUser(_ uuid.UUID, userID uuid.UUID, msg ws.Message) {
	var env ws.Envelope
	_ = json.Unmarshal(msg.Bytes(), &env)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sent = append(h.sent, sent{userID: userID, env: env})
}

func (h *targetHub) events() []sent {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.sent)
}

var testConfigDefaults = config.PromptTestConfig{MaxPromptBytes: 100, MaxConcurrent: 1, StaleAfter: 5 * time.Minute}

type promptFixture struct {
	*engineFixture
	exec  *aitest.Client
	hub   *targetHub
	logs  *syncLog
	svc   *PromptTestService
	round domain.GameProgress
}

// newPromptFixture starts round 1 of a game whose claude-sonnet-5 setting is
// mapped to the fake provider's "sonnet" model.
func newPromptFixture(t *testing.T, cfg config.PromptTestConfig, steps ...aitest.Step) *promptFixture {
	t.Helper()
	f := &promptFixture{engineFixture: newEngineFixture(t, 3), exec: aitest.New("fake", steps...), hub: &targetHub{}, logs: &syncLog{}}
	f.svc = NewPromptTestService(f.store, mustExecutor(t, f.exec), f.hub, f.clock, log.New(f.logs, "", 0), cfg)
	t.Cleanup(f.svc.Close)
	f.round = f.start(t)
	return f
}

// finished waits for a test to reach a terminal status.
func (f *promptFixture) finished(t *testing.T, id uuid.UUID) domain.PromptTest {
	t.Helper()
	waitFor(t, "the test to finish", func() bool { return f.store.promptTest(id).Status.Terminal() })
	// The event follows the write; wait for it too.
	waitFor(t, "the final event", func() bool {
		for _, e := range f.hub.events() {
			if e.env.Type == ws.TypeOutputComplete || e.env.Type == ws.TypeOutputError {
				var p struct {
					TestID string `json:"test_id"`
				}
				_ = json.Unmarshal(e.env.Payload, &p)
				if p.TestID == id.String() {
					return true
				}
			}
		}
		return false
	})
	return f.store.promptTest(id)
}

func (f *promptFixture) startTest(t *testing.T, userID uuid.UUID) *domain.PromptTest {
	t.Helper()
	test, err := f.svc.Start(context.Background(), userID, f.round.RoundID.String(), "write a haiku")
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	return test
}

func TestPromptTestStreamsPrivatelyAndIsStored(t *testing.T) {
	f := newPromptFixture(t, testConfigDefaults, aitest.Step{
		Chunks:   []string{"Hel", "lo"},
		Response: ai.Response{Usage: ai.Usage{InputTokens: 12, OutputTokens: 34}, FinishReason: "end_turn"},
	})

	test := f.startTest(t, f.guest)
	if test.Status != domain.PromptTestPending || test.TestNumber != 1 || test.ID == uuid.Nil {
		t.Fatalf("Start() = %+v, want pending test 1 with an ID", test)
	}
	got := f.finished(t, test.ID)
	if got.Status != domain.PromptTestCompleted || got.Output == nil || *got.Output != "Hello" || got.Prompt != "write a haiku" {
		t.Fatalf("stored test = %+v", got)
	}
	if *got.InputTokens != 12 || *got.OutputTokens != 34 || got.Provider != "fake" || got.Model != "sonnet" ||
		got.StartedAt == nil || got.CompletedAt == nil || got.LatencyMs == nil || got.ErrorCode != "" {
		t.Errorf("stored test details = %+v", got)
	}

	events := f.hub.events()
	if f.hub.broadcasts != 0 {
		t.Errorf("%d room broadcasts, want none", f.hub.broadcasts)
	}
	var types []ws.Type
	for _, e := range events {
		if e.userID != f.guest {
			t.Errorf("event %s sent to %s, want only the tester", e.env.Type, e.userID)
		}
		types = append(types, e.env.Type)
	}
	if !slices.Equal(types, []ws.Type{ws.TypeOutputChunk, ws.TypeOutputChunk, ws.TypeOutputComplete}) {
		t.Fatalf("events = %v", types)
	}
	var chunk ws.OutputChunkPayload
	_ = json.Unmarshal(events[1].env.Payload, &chunk)
	if chunk.Seq != 2 || chunk.Delta != "lo" || chunk.TestID != test.ID.String() || chunk.TestNumber != 1 {
		t.Errorf("second chunk = %+v", chunk)
	}
	var done ws.OutputCompletePayload
	_ = json.Unmarshal(events[2].env.Payload, &done)
	if done.Output != "Hello" || done.Status != "completed" || done.Usage == nil || done.Usage.OutputTokens != 34 || done.TestID != test.ID.String() {
		t.Errorf("output_complete = %+v", done)
	}
	if req := f.exec.Requests()[0]; req.Model != "sonnet" || req.Metadata["test_id"] != test.ID.String() || req.Messages[0].Content != "write a haiku" {
		t.Errorf("executor request = %+v", req)
	}

	if next := f.startTest(t, f.guest); next.TestNumber != 2 {
		t.Errorf("second test number = %d, want 2", next.TestNumber)
	}
}

func TestPromptTestRefusals(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid prompts", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults)
		for name, prompt := range map[string]string{"blank": "   ", "too long": string(make([]byte, 101)), "not UTF-8": "\xff\xfe"} {
			var vErr *ValidationError
			if _, err := f.svc.Start(ctx, f.guest, f.round.RoundID.String(), prompt); !errors.As(err, &vErr) || vErr.Fields["prompt"] == "" {
				t.Errorf("%s: error = %v, want a prompt validation error", name, err)
			}
		}
	})

	t.Run("who and which round", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults)
		for name, tc := range map[string]struct {
			user  uuid.UUID
			round string
			want  error
		}{
			"stranger":        {uuid.New(), f.round.RoundID.String(), domain.ErrNotGamePlayer},
			"unknown round":   {f.guest, uuid.NewString(), domain.ErrNotFound},
			"malformed round": {f.guest, "round-1", domain.ErrNotFound},
		} {
			if _, err := f.svc.Start(ctx, tc.user, tc.round, "p"); !errors.Is(err, tc.want) {
				t.Errorf("%s: error = %v, want %v", name, err, tc.want)
			}
		}
	})

	t.Run("round not active", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults)
		f.clock.Jump(f.round.RoundDeadline.Sub(f.clock.Now())) // deadline reached, timer not yet fired
		if _, err := f.svc.Start(ctx, f.guest, f.round.RoundID.String(), "p"); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("at the deadline: error = %v, want ErrRoundNotActive", err)
		}
		f.clock.Advance(0) // fire the timer: Judging
		if _, err := f.svc.Start(ctx, f.guest, f.round.RoundID.String(), "p"); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("in Judging: error = %v, want ErrRoundNotActive", err)
		}
		if err := f.engine.CompleteJudging(ctx, f.game.ID, f.round.RoundID); err != nil {
			t.Fatal(err)
		}
		if err := f.engine.Advance(ctx, f.game.ID, f.round.RoundID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Start(ctx, f.guest, f.round.RoundID.String(), "p"); !errors.Is(err, domain.ErrRoundNotActive) {
			t.Errorf("an earlier round: error = %v, want ErrRoundNotActive", err)
		}
	})

	t.Run("AI disabled", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults)
		svc := NewPromptTestService(f.store, nil, f.hub, f.clock, log.New(io.Discard, "", 0), testConfigDefaults)
		if _, err := svc.Start(ctx, f.guest, f.round.RoundID.String(), "p"); !errors.Is(err, domain.ErrAIUnavailable) {
			t.Errorf("error = %v, want ErrAIUnavailable", err)
		}
	})

	t.Run("nothing is stored for a refusal", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults)
		_, _ = f.svc.Start(ctx, uuid.New(), f.round.RoundID.String(), "p")
		_, _ = f.svc.Start(ctx, f.guest, f.round.RoundID.String(), "")
		if n := len(f.store.promptTests); n != 0 || f.exec.Calls() != 0 {
			t.Errorf("stored %d tests and called the AI %d times, want neither", n, f.exec.Calls())
		}
	})
}

func TestPromptTestConcurrencyLimitIsPerPlayer(t *testing.T) {
	f := newPromptFixture(t, testConfigDefaults, aitest.Step{Block: true})
	first := f.startTest(t, f.guest)

	if _, err := f.svc.Start(context.Background(), f.guest, f.round.RoundID.String(), "again"); !errors.Is(err, domain.ErrTestInProgress) {
		t.Fatalf("second concurrent test error = %v, want ErrTestInProgress", err)
	}
	other := f.startTest(t, f.host) // another player is not limited by the guest's test

	f.svc.Close() // cancels both running tests
	for _, id := range []uuid.UUID{first.ID, other.ID} {
		if got := f.store.promptTest(id); got.Status != domain.PromptTestCanceled || got.ErrorCode != TestErrorCanceled {
			t.Errorf("test after shutdown = %s/%s, want canceled/CANCELED", got.Status, got.ErrorCode)
		}
	}

	// A finished test no longer counts, and numbers are never reused.
	restarted := NewPromptTestService(f.store, mustExecutor(t, aitest.New("fake")), f.hub, f.clock, log.New(io.Discard, "", 0), testConfigDefaults)
	t.Cleanup(restarted.Close)
	next, err := restarted.Start(context.Background(), f.guest, f.round.RoundID.String(), "p")
	if err != nil || next.TestNumber != 2 {
		t.Fatalf("Start() after the first finished = %+v, %v; want test 2", next, err)
	}
}

func TestPromptTestNumbersAreGapFreeUnderConcurrency(t *testing.T) {
	cfg := testConfigDefaults
	cfg.MaxConcurrent = 100
	f := newPromptFixture(t, cfg, aitest.Step{Response: ai.Response{Text: "ok"}})

	const tests = 20
	numbers := make(chan int, tests)
	var wg sync.WaitGroup
	for range tests {
		wg.Go(func() {
			test, err := f.svc.Start(context.Background(), f.guest, f.round.RoundID.String(), "p")
			if err != nil {
				t.Errorf("Start() error = %v", err)
				return
			}
			numbers <- test.TestNumber
		})
	}
	wg.Wait()
	close(numbers)
	var got []int
	for n := range numbers {
		got = append(got, n)
	}
	slices.Sort(got)
	for i, n := range got {
		if n != i+1 {
			t.Fatalf("numbers = %v, want 1..%d with no gaps or duplicates", got, tests)
		}
	}
}

func TestPromptTestFailures(t *testing.T) {
	t.Run("a failure after output keeps it and the number", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults,
			aitest.Step{Chunks: []string{"par"}, StreamErr: &ai.AIError{Kind: ai.ErrKindUnavailable, Provider: "fake", Message: "reset by peer 10.0.0.1"}},
			aitest.Step{Response: ai.Response{Text: "fine"}},
		)
		first := f.finished(t, f.startTest(t, f.guest).ID)
		if first.Status != domain.PromptTestFailed || first.ErrorCode != "UNAVAILABLE" || first.Output == nil || *first.Output != "par" {
			t.Fatalf("failed test = %+v, want failed/UNAVAILABLE with the partial output", first)
		}
		last := f.hub.events()[len(f.hub.events())-1]
		var p ws.OutputErrorPayload
		_ = json.Unmarshal(last.env.Payload, &p)
		if last.env.Type != ws.TypeOutputError || p.Status != "failed" || p.ErrorCode != "UNAVAILABLE" || p.Message == "" {
			t.Errorf("final event = %s %+v", last.env.Type, p)
		}
		if p.Message == "reset by peer 10.0.0.1" {
			t.Error("the provider's message reached the player")
		}
		if second := f.finished(t, f.startTest(t, f.guest).ID); second.TestNumber != 2 || second.Status != domain.PromptTestCompleted {
			t.Errorf("next test = %+v, want test 2 completed", second)
		}
	})

	t.Run("a failure before any output stores none", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults, aitest.Step{Err: &ai.AIError{Kind: ai.ErrKindAuth, Provider: "fake"}})
		got := f.finished(t, f.startTest(t, f.guest).ID)
		if got.Status != domain.PromptTestFailed || got.ErrorCode != "AUTH" || got.Output != nil || got.InputTokens != nil {
			t.Errorf("failed test = %+v", got)
		}
	})

	t.Run("the round deadline cancels a running test", func(t *testing.T) {
		f := newPromptFixture(t, testConfigDefaults, aitest.Step{Block: true})
		f.clock.Jump(f.round.RoundDeadline.Sub(f.clock.Now()) - 50*time.Millisecond)
		got := f.finished(t, f.startTest(t, f.guest).ID)
		if got.Status != domain.PromptTestCanceled || got.ErrorCode != TestErrorRoundEnded {
			t.Errorf("test = %s/%s, want canceled/ROUND_ENDED", got.Status, got.ErrorCode)
		}
	})
}

func TestPromptTestListReturnsOnlyTheCallersTests(t *testing.T) {
	ctx := context.Background()
	cfg := testConfigDefaults
	cfg.MaxConcurrent = 10
	f := newPromptFixture(t, cfg, aitest.Step{Response: ai.Response{Text: "out"}})
	for _, player := range []uuid.UUID{f.host, f.host, f.guest} {
		f.finished(t, f.startTest(t, player).ID)
	}

	guests, err := f.svc.List(ctx, f.guest, f.round.RoundID.String())
	if err != nil || len(guests) != 1 || guests[0].PlayerID != f.guest {
		t.Fatalf("List(guest) = %+v, %v; want only the guest's test", guests, err)
	}
	hosts, _ := f.svc.List(ctx, f.host, f.round.RoundID.String())
	if len(hosts) != 2 || hosts[0].TestNumber != 1 || hosts[1].TestNumber != 2 {
		t.Errorf("List(host) = %+v, want tests 1 and 2 in order", hosts)
	}
	if _, err := f.svc.List(ctx, uuid.New(), f.round.RoundID.String()); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("List(stranger) error = %v, want ErrNotGamePlayer", err)
	}

	// Past rounds stay readable once the game moves on.
	f.clock.Advance(f.round.RoundDeadline.Sub(f.clock.Now()))
	if err := f.engine.CompleteJudging(ctx, f.game.ID, f.round.RoundID); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.Advance(ctx, f.game.ID, f.round.RoundID); err != nil {
		t.Fatal(err)
	}
	if past, err := f.svc.List(ctx, f.host, f.round.RoundID.String()); err != nil || len(past) != 2 {
		t.Errorf("List(past round) = %d tests, %v; want 2", len(past), err)
	}
}

func TestPromptTestRecoverFailsStaleTests(t *testing.T) {
	f := newPromptFixture(t, testConfigDefaults)
	now := f.clock.Now()
	stale := &domain.PromptTest{GameID: f.game.ID, RoundID: f.round.RoundID, PlayerID: f.guest, TestNumber: 1, Status: domain.PromptTestStreaming, CreatedAt: now.Add(-time.Hour)}
	fresh := &domain.PromptTest{GameID: f.game.ID, RoundID: f.round.RoundID, PlayerID: f.host, TestNumber: 1, Status: domain.PromptTestPending, CreatedAt: now}
	repo := fakePromptTestRepo{f.store}
	_ = repo.Create(context.Background(), stale)
	_ = repo.Create(context.Background(), fresh)
	f.store.promptTests[stale.ID].Status = domain.PromptTestStreaming

	if err := f.svc.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := f.store.promptTest(stale.ID); got.Status != domain.PromptTestFailed || got.ErrorCode != TestErrorInterrupted {
		t.Errorf("stale test = %s/%s, want failed/INTERRUPTED", got.Status, got.ErrorCode)
	}
	if got := f.store.promptTest(fresh.ID); got.Status != domain.PromptTestPending {
		t.Errorf("fresh test = %s, want it left alone", got.Status)
	}
}

func TestEngineRecordsEveryRound(t *testing.T) {
	ctx := context.Background()
	f := newEngineFixture(t, 3)
	first := f.start(t)
	f.judged(t)
	if err := f.engine.Advance(ctx, f.game.ID, first.RoundID); err != nil {
		t.Fatal(err)
	}
	second := f.progress()

	for _, p := range []domain.GameProgress{first, second} {
		round, ok := f.store.rounds[p.RoundID]
		if !ok || round.GameID != f.game.ID || round.Number != p.CurrentRound || !round.Deadline.Equal(p.RoundDeadline) {
			t.Errorf("round %d record = %+v (found %v)", p.CurrentRound, round, ok)
		}
	}
	if len(f.store.rounds) != 2 {
		t.Errorf("%d rounds recorded, want 2", len(f.store.rounds))
	}
}

// mustExecutor builds a real orchestrator over client, with the game's
// claude-sonnet-5 setting mapped to the "sonnet" model.
func mustExecutor(t *testing.T, client *aitest.Client) *ai.Orchestrator {
	t.Helper()
	key := ai.NewAPIKey("sk-test")
	orch, err := ai.NewOrchestrator(ai.Config{
		Executor:       ai.RoleConfig{Target: ai.Target{Provider: "fake", Model: "default"}, Timeout: time.Minute, MaxTokens: 100},
		Judge:          ai.RoleConfig{Target: ai.Target{Provider: "fake", Model: "judge"}, Timeout: time.Minute, MaxTokens: 100},
		ExecutorModels: map[string]ai.Target{string(domain.AIModelClaudeSonnet5): {Provider: "fake", Model: "sonnet"}},
		Retry:          ai.RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		Providers:      map[string]ai.ProviderSettings{"fake": {APIKey: key}},
	}, map[string]ai.AIClient{"fake": client}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return orch
}
