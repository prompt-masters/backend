package service

import (
	"context"
	"io"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/ai/aitest"
	"github.com/prompt-masters/backend/internal/domain"
)

var judgeChallenge = &domain.Challenge{
	Title:       "Rain haiku",
	Description: "Get the model to write a haiku about rain.",
	Constraints: domain.ChallengeConstraints{RequiredElements: []string{"rain"}},
	JudgeCriteria: domain.JudgeCriteria{PassingScore: 50, Criteria: []domain.JudgeCriterion{
		{Name: "quality", Description: "Is it good?", Weight: 0.7, MaxScore: 10},
		{Name: "form", Description: "Is it a haiku?", Weight: 0.3, MaxScore: 5},
	}},
}

type fixedChallenge struct{ c *domain.Challenge }

func (f fixedChallenge) ChallengeForRound(context.Context, uuid.UUID, uuid.UUID) (*domain.Challenge, error) {
	return f.c, nil
}

// syncLog is a log sink safe for the judging goroutine.
type syncLog struct {
	mu sync.Mutex
	sb strings.Builder
}

func (l *syncLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.Write(p)
}

func (l *syncLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sb.String()
}

type judgeFixture struct {
	*engineFixture
	exec  *aitest.Client
	judge *aitest.Client
	logs  *syncLog
}

// newJudgeFixture wires a real ai.Orchestrator over fake providers into the
// engine as its judge. judgeSteps scripts the judge from the players' IDs.
func newJudgeFixture(t *testing.T, challenges RoundChallenges, execSteps []aitest.Step, judgeSteps func(host, guest uuid.UUID) []aitest.Step) *judgeFixture {
	t.Helper()
	f := &judgeFixture{engineFixture: newEngineFixture(t, 3), logs: &syncLog{}}
	f.exec = aitest.New("fake", execSteps...)
	f.judge = aitest.New("fakejudge", judgeSteps(f.host, f.guest)...)
	f.engine.SetJudge(NewRoundJudge(mustOrchestrator(t, f), f.store, challenges, f.engine, log.New(f.logs, "", 0)))
	return f
}

func judgement(host, guest uuid.UUID) []aitest.Step {
	return []aitest.Step{{Response: ai.Response{Text: `{"results":[` +
		`{"candidate_id":"` + host.String() + `","scores":{"quality":8,"form":5},"feedback":"good"},` +
		`{"candidate_id":"` + guest.String() + `","scores":{"quality":2,"form":1},"feedback":"weak"}]}`}}}
}

// endRound submits for both players, ending the round early.
func (f *judgeFixture) endRound(t *testing.T) domain.GameProgress {
	t.Helper()
	round := f.start(t)
	for _, player := range []uuid.UUID{f.host, f.guest} {
		if err := f.submit(player); err != nil {
			t.Fatalf("Submit() error = %v", err)
		}
	}
	return round
}

// waitFor polls cond, since judging runs on its own goroutine.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestRoundJudgeRunsWhenTheRoundIsJudged(t *testing.T) {
	f := newJudgeFixture(t, fixedChallenge{judgeChallenge}, []aitest.Step{{Response: ai.Response{Text: "rain on the roof"}}}, judgement)
	round := f.endRound(t)

	waitFor(t, "judging to complete", func() bool { return f.progress().Phase == domain.GamePhaseResults })
	if f.exec.Calls() != 2 || f.judge.Calls() != 1 {
		t.Errorf("executor calls = %d, judge calls = %d; want 2 and 1", f.exec.Calls(), f.judge.Calls())
	}
	for _, req := range f.exec.Requests() {
		if req.Model != "sonnet" || req.Messages[0].Content != "my prompt" || req.Metadata["round_id"] != round.RoundID.String() {
			t.Errorf("executor request = %+v, want the game's ai_model mapped through config", req)
		}
	}
	prompt := f.judge.Requests()[0].Messages[0].Content
	if !strings.Contains(prompt, "rain on the roof") || !strings.Contains(prompt, "Rain haiku") {
		t.Errorf("judge prompt lacks the outputs or the task:\n%s", prompt)
	}
	// 0.7*8/10 + 0.3*5/5 = 0.86; 0.7*2/10 + 0.3*1/5 = 0.20
	if logs := f.logs.String(); !strings.Contains(logs, "total=86.0") || !strings.Contains(logs, "total=20.0") {
		t.Errorf("logs = %s, want the rubric-weighted totals", logs)
	}
}

func TestRoundJudgeNeverHoldsTheGameLock(t *testing.T) {
	f := newJudgeFixture(t, fixedChallenge{judgeChallenge},
		[]aitest.Step{{Block: true}}, // the executor hangs until the engine closes
		judgement)
	round := f.endRound(t) // returns although judging has started and is stuck

	waitFor(t, "judging to start", func() bool { return f.exec.Calls() > 0 })
	if p := f.progress(); p.Phase != domain.GamePhaseJudging {
		t.Fatalf("phase = %s, want Judging", p.Phase)
	}
	// Transitions still go through while the AI call is in flight.
	done := make(chan error, 1)
	go func() { done <- f.engine.CompleteJudging(context.Background(), f.game.ID, round.RoundID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CompleteJudging() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a transition waited on the AI call")
	}

	// Closing the engine cancels the stuck call instead of waiting on it.
	closed := make(chan struct{})
	go func() { f.engine.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close() waited on the AI call")
	}
}

func TestRoundJudgeCompletesEvenWhenTheAIFails(t *testing.T) {
	failing := []aitest.Step{{Err: &ai.AIError{Kind: ai.ErrKindAuth, Provider: "fake", Message: "bad key"}}}
	f := newJudgeFixture(t, fixedChallenge{judgeChallenge}, failing, func(uuid.UUID, uuid.UUID) []aitest.Step { return failing })
	f.endRound(t)

	waitFor(t, "judging to complete", func() bool { return f.progress().Phase == domain.GamePhaseResults })
	if logs := f.logs.String(); !strings.Contains(logs, "op=execute") || !strings.Contains(logs, "op=score") || !strings.Contains(logs, "error_kind=auth") {
		t.Errorf("logs = %s, want the failures logged", logs)
	}
}

func TestRoundJudgeSkipsRoundsWithoutAChallenge(t *testing.T) {
	f := newJudgeFixture(t, nil, nil, judgement)
	round := f.endRound(t)

	waitFor(t, "the skip", func() bool { return strings.Contains(f.logs.String(), "reason=rounds_have_no_challenge") })
	if !strings.Contains(f.logs.String(), round.RoundID.String()) {
		t.Errorf("skip log lacks the round: %s", f.logs.String())
	}
	if p := f.progress(); p.Phase != domain.GamePhaseJudging || f.exec.Calls()+f.judge.Calls() != 0 {
		t.Errorf("phase = %s, AI calls = %d; want the round left in Judging untouched", p.Phase, f.exec.Calls()+f.judge.Calls())
	}
}

func TestRecoverJudgesRoundsLeftInJudging(t *testing.T) {
	f := newJudgeFixture(t, fixedChallenge{judgeChallenge}, []aitest.Step{{Response: ai.Response{Text: "out"}}}, judgement)
	f.engine.SetJudge(nil) // as if the server stopped mid-judging
	f.endRound(t)
	if p := f.progress(); p.Phase != domain.GamePhaseJudging {
		t.Fatalf("phase = %s, want Judging", p.Phase)
	}

	restarted := NewGameEngine(f.store, f.clock, f.events, f.live, log.New(io.Discard, "", 0))
	t.Cleanup(restarted.Close)
	restarted.SetJudge(NewRoundJudge(mustOrchestrator(t, f), f.store, fixedChallenge{judgeChallenge}, restarted, log.New(f.logs, "", 0)))
	if err := restarted.Recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "recovered judging", func() bool { return f.progress().Phase == domain.GamePhaseResults })
}

// mustOrchestrator builds a real orchestrator over f's fake providers, with
// the game's claude-sonnet-5 setting mapped to the "sonnet" model.
func mustOrchestrator(t *testing.T, f *judgeFixture) *ai.Orchestrator {
	t.Helper()
	key := ai.NewAPIKey("sk-test")
	orch, err := ai.NewOrchestrator(ai.Config{
		Executor:       ai.RoleConfig{Target: ai.Target{Provider: "fake", Model: "default"}, Timeout: time.Minute, MaxTokens: 100},
		Judge:          ai.RoleConfig{Target: ai.Target{Provider: "fakejudge", Model: "judge"}, Timeout: time.Minute, MaxTokens: 100},
		ExecutorModels: map[string]ai.Target{string(domain.AIModelClaudeSonnet5): {Provider: "fake", Model: "sonnet"}},
		Retry:          ai.RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		Providers:      map[string]ai.ProviderSettings{"fake": {APIKey: key}, "fakejudge": {APIKey: key}},
	}, map[string]ai.AIClient{"fake": f.exec, "fakejudge": f.judge}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return orch
}
