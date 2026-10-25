package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/ai"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

var candidatePattern = regexp.MustCompile(`<candidate id="([A-Z]+)">\n(.*)\n</candidate>`)

// judgeFake is an AI provider playing the judge: it reads the anonymous
// candidates from the request and answers through reply.
type judgeFake struct {
	mu       sync.Mutex
	requests []ai.Request
	// reply builds the judge's text from each label's prompt.
	reply func(prompts map[string]string) string
	// hold, when set, is waited on before answering.
	hold  chan struct{}
	delay time.Duration
}

func (f *judgeFake) Generate(ctx context.Context, req ai.Request) (ai.Response, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	reply, hold := f.reply, f.hold
	f.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ai.Response{}, ai.ContextError(ctx, "fakejudge")
		}
	}
	time.Sleep(f.delay)
	prompts := map[string]string{}
	for _, m := range candidatePattern.FindAllStringSubmatch(req.Messages[0].Content, -1) {
		var c struct{ Prompt string }
		_ = json.Unmarshal([]byte(m[2]), &c)
		prompts[m[1]] = c.Prompt
	}
	return ai.Response{Text: reply(prompts), Model: req.Model, Usage: ai.Usage{InputTokens: 100, OutputTokens: 50}}, nil
}

func (f *judgeFake) Stream(context.Context, ai.Request) (<-chan ai.StreamEvent, error) {
	return nil, errors.New("the judge does not stream")
}

func (f *judgeFake) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *judgeFake) setReply(reply func(map[string]string) string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reply = reply
}

// scoresByPrompt answers with fixed scores per prompt: accuracy, simplicity,
// creativity, adherence.
func scoresByPrompt(scores map[string][4]float64) func(map[string]string) string {
	return func(prompts map[string]string) string {
		var results []string
		for label, prompt := range prompts {
			s := scores[prompt]
			results = append(results, fmt.Sprintf(`{"candidate_id":%q,"scores":{"accuracy":%g,"simplicity":%g,"creativity":%g,"adherence":%g},"feedback":"on %s"}`,
				label, s[0], s[1], s[2], s[3], label))
		}
		return `{"results":[` + strings.Join(results, ",") + `]}`
	}
}

type judgingFixture struct {
	*engineFixture
	judge  *judgeFake
	logs   *syncLog
	rj     *RoundJudge
	rounds *RoundService
}

var judgingConfig = config.JudgingConfig{MaxRuns: 2, RetryDelay: time.Millisecond, StaleAfter: time.Minute, Weights: domain.DefaultScoreWeights}

// newJudgingFixture wires a RoundJudge over a real orchestrator and the fake
// judge into a two-player game on a challenge with a rubric.
func newJudgingFixture(t *testing.T, rounds int, reply func(map[string]string) string) *judgingFixture {
	t.Helper()
	f := &judgingFixture{engineFixture: newEngineFixture(t, rounds), judge: &judgeFake{reply: reply}, logs: &syncLog{}}
	f.store.usernames[f.host], f.store.usernames[f.guest] = "alice-host", "bob-guest"
	f.store.mu.Lock()
	for i := range f.store.games[f.game.ID].Players {
		p := &f.store.games[f.game.ID].Players[i]
		p.Username = f.store.usernames[p.UserID]
	}
	c := f.store.challenges.challenges[0]
	c.Title, c.Description = "FizzBuzz", "Get the model to write FizzBuzz."
	c.JudgeCriteria = domain.JudgeCriteria{PassingScore: 50, Criteria: []domain.JudgeCriterion{{Name: "correctness", Description: "It works.", Weight: 1, MaxScore: 10}}}
	f.store.mu.Unlock()

	key := ai.NewAPIKey("sk-test")
	fake := ai.Target{Provider: "fakejudge", Model: "judge-model"}
	orch, err := ai.NewOrchestrator(ai.Config{
		Executor:          ai.RoleConfig{Target: fake, Timeout: time.Minute, MaxTokens: 100},
		Judge:             ai.RoleConfig{Target: fake, Timeout: time.Minute, MaxTokens: 100},
		Retry:             ai.RetryConfig{MaxAttempts: 1, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		Providers:         map[string]ai.ProviderSettings{"fakejudge": {APIKey: key}},
		JudgeParseRetries: 1,
	}, map[string]ai.AIClient{"fakejudge": f.judge}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	f.rj = NewRoundJudge(orch, f.store, f.engine, f.events, f.clock, &sequenceRandom{values: []int{1, 0, 2}}, log.New(f.logs, "", 0), judgingConfig)
	f.engine.SetJudge(f.rj)
	f.rounds = NewRoundService(f.store, f.engine)
	return f
}

// submitAs runs a completed test with prompt for userID and submits it.
func (f *judgingFixture) submitAs(t *testing.T, userID uuid.UUID, prompt string) *domain.Submission {
	t.Helper()
	test := f.addTest(userID, domain.PromptTestCompleted, prompt, prompt+" -> output")
	sub, _, err := f.engine.Submit(context.Background(), f.game.ID, userID, f.progress().RoundID, test.ID)
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	return sub
}

func (f *judgingFixture) scores(roundID uuid.UUID) []domain.Score {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	s, _ := fakeJudgingRepo{f.store}.ListScores(context.Background(), roundID)
	return s
}

func (f *judgingFixture) run(roundID uuid.UUID) *domain.JudgingRun {
	f.store.mu.Lock()
	defer f.store.mu.Unlock()
	run, _ := fakeJudgingRepo{f.store}.Get(context.Background(), roundID)
	return run
}

func (f *judgingFixture) inPhase(phase domain.GamePhase) func() bool {
	return func() bool { return f.progress().Phase == phase }
}

func (f *judgingFixture) count(kind string) int {
	n := 0
	for _, e := range f.events.events() {
		if e.kind == kind {
			n++
		}
	}
	return n
}

var twoPlayerScores = scoresByPrompt(map[string][4]float64{
	"host prompt":  {8, 7, 9, 8},
	"guest prompt": {5, 5, 5, 5},
})

func TestJudgingScoresRanksAndPublishes(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	round := f.start(t)
	hostSub := f.submitAs(t, f.host, "host prompt")
	f.addTest(f.guest, domain.PromptTestFailed, "try", "") // an attempt that counts
	guestSub := f.submitAs(t, f.guest, "guest prompt")

	waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
	waitFor(t, "the results broadcast", func() bool { return f.count("round_results") == 1 })

	scores := f.scores(round.RoundID)
	if len(scores) != 2 {
		t.Fatalf("stored %d scores, want 2", len(scores))
	}
	host, guest := scores[0], scores[1]
	// Host: efficiency 10 (one test, short prompt); total (8+7+9+8+10)/5.
	if host.PlayerID != f.host || host.Rank != 1 || host.Total != 840 || host.Components.Efficiency != 1000 || host.SubmissionID != hostSub.ID {
		t.Errorf("host score = %+v", host)
	}
	// Guest: two tests, so efficiency (9+10)/2; total (5*4+9.5)/5.
	if guest.PlayerID != f.guest || guest.Rank != 2 || guest.Total != 590 || guest.Components.Efficiency != 950 || guest.SubmissionID != guestSub.ID {
		t.Errorf("guest score = %+v", guest)
	}
	if labels := []string{host.Label, guest.Label}; !slices.Contains(labels, "A") || !slices.Contains(labels, "B") {
		t.Errorf("labels = %v, want A and B", labels)
	}
	if host.Provider != "fakejudge" || host.Model != "judge-model" || host.Rationale == "" {
		t.Errorf("host score details = %+v", host)
	}
	if run := f.run(round.RoundID); run.Status != domain.JudgingCompleted || run.Attempt != 1 {
		t.Errorf("run = %+v", run)
	}

	var results *domain.RoundResults
	stateIdx, resultsIdx := -1, -1
	for i, e := range f.events.events() {
		if e.kind == "game_state" && e.progress.Phase == domain.GamePhaseResults {
			stateIdx = i
		}
		if e.kind == "round_results" {
			resultsIdx, results = i, e.results
		}
	}
	if stateIdx < 0 || resultsIdx < stateIdx {
		t.Errorf("round_results at %d, Results state at %d: want results after the transition", resultsIdx, stateIdx)
	}
	if r := results.Results; len(r) != 2 || r[0].PlayerID != f.host || r[0].Rank != 1 || r[0].Prompt != "host prompt" ||
		r[0].Output != "host prompt -> output" || r[0].DisplayName != "alice-host" || r[1].TestCount != 2 || r[1].Source != domain.SubmissionManual {
		t.Errorf("round_results = %+v", results)
	}
	if results.RoundID != round.RoundID || results.Number != 1 {
		t.Errorf("round_results round = %s/%d", results.RoundID, results.Number)
	}
}

func TestJudgeRequestCarriesNoIdentity(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	round := f.start(t)
	hostSub := f.submitAs(t, f.host, "host prompt")
	guestSub := f.submitAs(t, f.guest, "guest prompt")
	waitFor(t, "results", f.inPhase(domain.GamePhaseResults))

	// Everything the provider receives (metadata never leaves the server).
	req := f.judge.requests[0]
	body := req.Model + "\n" + req.System
	for _, m := range req.Messages {
		body += "\n" + string(m.Role) + ": " + m.Content
	}

	forbidden := []string{
		f.host.String(), f.guest.String(), "alice-host", "bob-guest",
		hostSub.ID.String(), guestSub.ID.String(), hostSub.TestID.String(), guestSub.TestID.String(),
		f.game.ID.String(), round.RoundID.String(),
		strconv.FormatInt(hostSub.SubmittedAt.UnixMilli(), 10), hostSub.SubmittedAt.Format(time.RFC3339),
	}
	for _, id := range forbidden {
		if strings.Contains(body, id) {
			t.Errorf("the judge request contains %q", id)
		}
	}
	for key := range req.Metadata {
		if strings.Contains(key, "user") || strings.Contains(key, "player") {
			t.Errorf("request metadata names a player: %v", req.Metadata)
		}
	}
	for _, want := range []string{`<candidate id="A">`, `<candidate id="B">`, "host prompt", "FizzBuzz", "correctness"} {
		if !strings.Contains(body, want) {
			t.Errorf("the judge request lacks %q", want)
		}
	}
}

func TestJudgingShufflesLabels(t *testing.T) {
	subs := []domain.Submission{{PlayerID: uuid.New()}, {PlayerID: uuid.New()}, {PlayerID: uuid.New()}}
	orders := map[string]bool{}
	for seed := range 6 {
		rj := &RoundJudge{random: &sequenceRandom{values: []int{seed, seed / 2, seed * 7}}}
		labeled := rj.shuffle(subs)
		var order []string
		for i, l := range labeled {
			if l.Label != label(i) {
				t.Fatalf("label %d = %q, want %q", i, l.Label, label(i))
			}
			order = append(order, l.PlayerID.String()[:4])
		}
		orders[strings.Join(order, ",")] = true
	}
	if len(orders) < 2 {
		t.Errorf("every shuffle gave the same order: %v", orders)
	}
	if label(0) != "A" || label(25) != "Z" || label(26) != "AA" {
		t.Errorf("labels = %s %s %s", label(0), label(25), label(26))
	}
}

func TestJudgingInvalidOutputFailsCleanly(t *testing.T) {
	bad := map[string]func(map[string]string) string{
		"missing label": func(map[string]string) string {
			return `{"results":[{"candidate_id":"A","scores":{"accuracy":1,"simplicity":1,"creativity":1,"adherence":1}}]}`
		},
		"out of range": func(p map[string]string) string {
			return strings.ReplaceAll(twoPlayerScores(p), `"accuracy":8`, `"accuracy":11`)
		},
		"extra label": func(p map[string]string) string {
			return strings.Replace(twoPlayerScores(p), `{"results":[`, `{"results":[{"candidate_id":"C","scores":{"accuracy":1,"simplicity":1,"creativity":1,"adherence":1}},`, 1)
		},
		"not JSON": func(map[string]string) string { return "A wins." },
	}
	for name, reply := range bad {
		t.Run(name, func(t *testing.T) {
			f := newJudgingFixture(t, 3, reply)
			round := f.start(t)
			f.submitAs(t, f.host, "host prompt")
			f.submitAs(t, f.guest, "guest prompt")

			waitFor(t, "judging to give up", func() bool { return f.count("judging_failed") == 1 })
			// Two runs, each asking twice (the orchestrator's parse retry).
			if calls := f.judge.calls(); calls != 4 {
				t.Errorf("judge calls = %d, want 4", calls)
			}
			if run := f.run(round.RoundID); run.Status != domain.JudgingFailed || run.ErrorCode != "INVALID_OUTPUT" {
				t.Errorf("run = %+v, want failed/INVALID_OUTPUT", run)
			}
			if n := len(f.scores(round.RoundID)); n != 0 {
				t.Errorf("%d partial scores stored", n)
			}
			if p := f.progress(); p.Phase != domain.GamePhaseJudging {
				t.Errorf("phase = %s, want the round kept in Judging", p.Phase)
			}
			for _, e := range f.events.events() {
				if e.kind == "judging_failed" && !e.retryable {
					t.Error("judging_failed says a retry cannot help")
				}
			}

			// The host retries once the judge behaves.
			ctx := context.Background()
			if err := f.rounds.RetryJudging(ctx, f.guest, round.RoundID.String()); !errors.Is(err, domain.ErrNotGameHost) {
				t.Errorf("RetryJudging(guest) error = %v, want ErrNotGameHost", err)
			}
			f.judge.setReply(twoPlayerScores)
			if err := f.rounds.RetryJudging(ctx, f.host, round.RoundID.String()); err != nil {
				t.Fatalf("RetryJudging(host) error = %v", err)
			}
			waitFor(t, "results after the retry", f.inPhase(domain.GamePhaseResults))
			if n := len(f.scores(round.RoundID)); n != 2 {
				t.Errorf("%d scores after the retry, want 2", n)
			}
			if err := f.rounds.RetryJudging(ctx, f.host, round.RoundID.String()); !errors.Is(err, domain.ErrJudgingNotFailed) {
				t.Errorf("RetryJudging() after success error = %v, want ErrJudgingNotFailed", err)
			}
		})
	}
}

func TestJudgingRunsOnceUnderConcurrency(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	f.judge.delay = 20 * time.Millisecond
	f.engine.SetJudge(nil) // start the workers by hand
	round := f.start(t)
	f.submitAs(t, f.host, "host prompt")
	f.submitAs(t, f.guest, "guest prompt")

	var wg sync.WaitGroup
	start := make(chan struct{})
	for range 20 {
		wg.Go(func() {
			<-start
			f.rj.JudgeRound(context.Background(), f.game.ID, round.RoundID)
		})
	}
	close(start)
	wg.Wait()

	if calls := f.judge.calls(); calls != 1 {
		t.Errorf("judge calls = %d, want 1", calls)
	}
	if n := len(f.scores(round.RoundID)); n != 2 {
		t.Errorf("stored %d scores, want one set of 2", n)
	}
	results := 0
	for _, e := range f.events.events() {
		if e.kind == "game_state" && e.progress.Phase == domain.GamePhaseResults {
			results++
		}
	}
	if results != 1 || f.count("round_results") != 1 {
		t.Errorf("Results transitions = %d, round_results = %d; want 1 and 1", results, f.count("round_results"))
	}
}

func TestJudgingRecoversAfterRestart(t *testing.T) {
	// restart builds a fresh engine and judge on the same store, as a new
	// server process would, and recovers.
	restart := func(t *testing.T, f *judgingFixture) {
		t.Helper()
		engine := NewGameEngine(f.store, f.clock, f.events, f.live, log.New(io.Discard, "", 0), EngineOptions{Random: f.random})
		t.Cleanup(engine.Close)
		rj := *f.rj
		rj.engine = engine
		engine.SetJudge(&rj)
		f.engine = engine
		if err := engine.Recover(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	judgingRound := func(t *testing.T) (*judgingFixture, domain.GameProgress) {
		f := newJudgingFixture(t, 3, twoPlayerScores)
		f.engine.SetJudge(nil) // the "old server" stops before judging
		round := f.start(t)
		f.submitAs(t, f.host, "host prompt")
		f.submitAs(t, f.guest, "guest prompt")
		return f, round
	}

	t.Run("never judged", func(t *testing.T) {
		f, round := judgingRound(t)
		restart(t, f)
		waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
		if len(f.scores(round.RoundID)) != 2 {
			t.Error("no scores after recovery")
		}
	})

	t.Run("failed run", func(t *testing.T) {
		f, round := judgingRound(t)
		f.store.judgingRuns[round.RoundID] = &domain.JudgingRun{RoundID: round.RoundID, GameID: f.game.ID, Status: domain.JudgingFailed, Attempt: 2, StartedAt: f.clock.Now()}
		restart(t, f)
		waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
		if run := f.run(round.RoundID); run.Attempt != 3 || run.Status != domain.JudgingCompleted {
			t.Errorf("run = %+v, want attempt 3 completed", run)
		}
	})

	t.Run("stale running run is taken over, a live one is not", func(t *testing.T) {
		f, round := judgingRound(t)
		f.store.judgingRuns[round.RoundID] = &domain.JudgingRun{RoundID: round.RoundID, GameID: f.game.ID, Status: domain.JudgingRunning, Attempt: 1, StartedAt: f.clock.Now()}
		restart(t, f)
		time.Sleep(50 * time.Millisecond)
		if f.judge.calls() != 0 || f.progress().Phase != domain.GamePhaseJudging {
			t.Fatalf("a run still held by its worker was judged again")
		}
		f.clock.Jump(2 * time.Minute) // past StaleAfter
		restart(t, f)
		waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
	})

	t.Run("scores committed but not published", func(t *testing.T) {
		f, round := judgingRound(t)
		state, attempt, err := f.rj.claim(context.Background(), f.game.ID, round.RoundID)
		if err != nil || state != claimTaken {
			t.Fatalf("claim() = %v, %v", state, err)
		}
		if err := f.rj.judge(context.Background(), f.game.ID, round.RoundID, attempt); err != nil {
			t.Fatal(err)
		}
		calls := f.judge.calls()
		restart(t, f)
		waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
		if f.judge.calls() != calls || len(f.scores(round.RoundID)) != 2 || f.count("round_results") != 1 {
			t.Errorf("judge calls %d → %d, scores %d, round_results %d; want published without judging again",
				calls, f.judge.calls(), len(f.scores(round.RoundID)), f.count("round_results"))
		}
	})
}

func TestTimerExpiryAutoSubmitsTheLastCompletedTest(t *testing.T) {
	f := newJudgingFixture(t, 3, scoresByPrompt(map[string][4]float64{"second": {6, 6, 6, 6}}))
	round := f.start(t)
	f.addTest(f.host, domain.PromptTestCompleted, "first", "first output")
	second := f.addTest(f.host, domain.PromptTestCompleted, "second", "second output")
	f.addTest(f.host, domain.PromptTestFailed, "third", "partial") // later, but failed
	f.addTest(f.guest, domain.PromptTestCanceled, "guest's", "")   // nothing completed

	f.clock.Advance(round.RoundDeadline.Sub(f.clock.Now()))
	subs := f.submissions(round.RoundID)
	if len(subs) != 1 {
		t.Fatalf("submissions = %+v, want the host's alone", subs)
	}
	if s := subs[0]; s.PlayerID != f.host || s.TestID != second.ID || s.Source != domain.SubmissionAuto ||
		s.Output != "second output" || s.TestCount != 3 || !s.LockedAt.Equal(round.RoundDeadline) {
		t.Errorf("auto submission = %+v", s)
	}

	waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
	scores := f.scores(round.RoundID)
	if len(scores) != 2 || scores[0].PlayerID != f.host || scores[1].PlayerID != f.guest ||
		!scores[1].NoSubmission || scores[1].Total != 0 || scores[1].Rank != 2 || scores[1].SubmissionID != uuid.Nil {
		t.Errorf("scores = %+v, want the guest last with a no_submission zero", scores)
	}
}

func TestTimerExpiryKeepsManualSubmissions(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	round := f.start(t)
	manual := f.submitAs(t, f.guest, "guest prompt")
	f.addTest(f.guest, domain.PromptTestCompleted, "later", "later output") // must not replace it
	f.addTest(f.host, domain.PromptTestCompleted, "host prompt", "out")

	f.clock.Advance(round.RoundDeadline.Sub(f.clock.Now()))
	for _, s := range f.submissions(round.RoundID) {
		if s.PlayerID == f.guest && (s.ID != manual.ID || s.Source != domain.SubmissionManual) {
			t.Errorf("guest's submission = %+v, want the manual one kept", s)
		}
		if s.PlayerID == f.host && s.Source != domain.SubmissionAuto {
			t.Errorf("host's submission = %+v, want auto", s)
		}
	}
}

func TestNobodySubmittedIsScoredWithoutTheJudge(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	round := f.start(t)
	f.clock.Advance(round.RoundDeadline.Sub(f.clock.Now()))

	waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
	scores := f.scores(round.RoundID)
	if f.judge.calls() != 0 || len(scores) != 2 || !scores[0].NoSubmission || scores[0].Rank != 1 || scores[1].Rank != 1 {
		t.Errorf("judge calls = %d, scores = %+v; want two shared no_submission ranks and no call", f.judge.calls(), scores)
	}
}

// TestManualSubmitRacingTheDeadline submits by hand as the round timer fires,
// many times over: the host always ends up with exactly one submission.
func TestManualSubmitRacingTheDeadline(t *testing.T) {
	outcomes := map[domain.SubmissionSource]int{}
	for i := range 200 {
		f := newEngineFixture(t, 3)
		round := f.start(t)
		chosen := f.addTest(f.host, domain.PromptTestCompleted, "chosen", "out")
		// A later completed test, which an auto-submission would pick instead.
		latest := f.addTest(f.host, domain.PromptTestCompleted, "latest", "out")
		f.clock.Jump(round.RoundDeadline.Sub(f.clock.Now()) - time.Millisecond)

		var (
			wg        sync.WaitGroup
			submitErr error
			start     = make(chan struct{})
		)
		wg.Go(func() { <-start; f.clock.Advance(time.Millisecond) })
		wg.Go(func() {
			<-start
			_, _, submitErr = f.engine.Submit(context.Background(), f.game.ID, f.host, round.RoundID, chosen.ID)
		})
		close(start)
		wg.Wait()
		waitFor(t, "judging", func() bool { return f.progress().Phase == domain.GamePhaseJudging })

		subs := f.submissions(round.RoundID)
		if len(subs) != 1 {
			t.Fatalf("iteration %d: %d submissions, want exactly 1", i, len(subs))
		}
		switch s := subs[0]; {
		case s.Source == domain.SubmissionManual && s.TestID == chosen.ID && submitErr == nil,
			s.Source == domain.SubmissionAuto && s.TestID == latest.ID && errors.Is(submitErr, domain.ErrRoundNotActive):
			outcomes[s.Source]++
		default:
			t.Fatalf("iteration %d: %s submission of %s with submit error %v", i, s.Source, s.TestID, submitErr)
		}
		f.engine.Close()
	}
	t.Logf("outcomes over 200 races: %v", outcomes)
}

func TestResultsAdvanceAndRoundsGetFreshChallenges(t *testing.T) {
	f := newEngineFixture(t, 2)
	f.store.challenges.challenges = append(f.store.challenges.challenges, newChallenge("coding-medium-2", domain.CategoryCoding, domain.DifficultyMedium))
	f.engine.Close()
	f.engine = NewGameEngine(f.store, f.clock, f.events, f.live, log.New(io.Discard, "", 0), EngineOptions{Random: f.random, ResultsDuration: 15 * time.Second})
	t.Cleanup(f.engine.Close)

	first := f.start(t)
	f.judged(t)
	f.clock.Advance(14 * time.Second)
	if p := f.progress(); p.Phase != domain.GamePhaseResults {
		t.Fatalf("phase = %s before the results time is up, want Results", p.Phase)
	}
	f.clock.Advance(time.Second)
	second := f.progress()
	if second.Phase != domain.GamePhaseRoundActive || second.CurrentRound != 2 {
		t.Fatalf("progress = %+v after results, want round 2", second)
	}
	if first.ChallengeID == uuid.Nil || second.ChallengeID == uuid.Nil || first.ChallengeID == second.ChallengeID {
		t.Errorf("challenges = %s then %s, want two different ones", first.ChallengeID, second.ChallengeID)
	}
	if r := f.store.rounds[second.RoundID]; r.ChallengeID != second.ChallengeID {
		t.Errorf("recorded round challenge = %s, want %s", r.ChallengeID, second.ChallengeID)
	}

	f.judged(t)
	f.clock.Advance(15 * time.Second)
	if p := f.progress(); p.Phase != domain.GamePhaseGameOver {
		t.Errorf("phase = %s after the last results, want GameOver", p.Phase)
	}
}

func TestSubmissionsStayPrivateUntilResults(t *testing.T) {
	f := newJudgingFixture(t, 3, twoPlayerScores)
	f.judge.hold = make(chan struct{})
	ctx := context.Background()
	f.start(t)
	hostSub := f.submitAs(t, f.host, "host prompt")
	guestSub := f.submitAs(t, f.guest, "guest prompt")
	waitFor(t, "judging to start", func() bool { return f.judge.calls() == 1 })

	if got, err := f.rounds.Get(ctx, f.host, hostSub.ID.String()); err != nil || got.Prompt != "host prompt" {
		t.Errorf("own submission = %+v, %v", got, err)
	}
	checks := map[string]error{}
	_, checks["opponent's submission"] = f.rounds.Get(ctx, f.host, guestSub.ID.String())
	_, checks["stranger"] = f.rounds.Get(ctx, uuid.New(), hostSub.ID.String())
	_, checks["opponent's score"] = f.rounds.Score(ctx, f.host, guestSub.ID.String())
	_, checks["unknown"] = f.rounds.Get(ctx, f.host, uuid.NewString())
	for name, err := range checks {
		if !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s before results: error = %v, want ErrNotFound", name, err)
		}
	}
	if _, err := f.rounds.Score(ctx, f.host, hostSub.ID.String()); !errors.Is(err, domain.ErrResultsNotReady) {
		t.Errorf("own score before results: error = %v, want ErrResultsNotReady", err)
	}

	close(f.judge.hold)
	waitFor(t, "results", f.inPhase(domain.GamePhaseResults))
	if got, err := f.rounds.Get(ctx, f.host, guestSub.ID.String()); err != nil || got.Prompt != "guest prompt" {
		t.Errorf("opponent's submission after results = %+v, %v", got, err)
	}
	score, err := f.rounds.Score(ctx, f.guest, hostSub.ID.String())
	if err != nil || score.Rank != 1 || score.Total != 840 || score.Rationale == "" {
		t.Errorf("opponent's score after results = %+v, %v", score, err)
	}
	if _, err := f.rounds.Get(ctx, uuid.New(), guestSub.ID.String()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("stranger after results: error = %v, want ErrNotFound", err)
	}
}

// TestSubmissionsCannotBeChanged pins the repository contract: there is no
// method that could update or delete a submission.
func TestSubmissionsCannotBeChanged(t *testing.T) {
	repo := reflect.TypeFor[repository.SubmissionRepository]()
	for i := range repo.NumMethod() {
		name := repo.Method(i).Name
		for _, verb := range []string{"Update", "Delete", "Set", "Edit", "Replace", "Remove"} {
			if strings.HasPrefix(name, verb) {
				t.Errorf("SubmissionRepository.%s could change a locked submission", name)
			}
		}
	}
}
