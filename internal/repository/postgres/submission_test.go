package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

// completedTest stores a completed prompt test for player in the fixture's
// round.
func (f *roundFixture) completedTest(t *testing.T, player uuid.UUID, number int, output string) *domain.PromptTest {
	t.Helper()
	ctx := context.Background()
	test := f.create(t, player, number, time.UnixMilli(1759230001000).UTC())
	if err := f.tests.Finish(ctx, test.ID, domain.PromptTestResult{Status: domain.PromptTestCompleted, Output: &output, CompletedAt: time.UnixMilli(1759230002000).UTC()}); err != nil {
		t.Fatal(err)
	}
	return test
}

func (f *roundFixture) submission(test *domain.PromptTest, source domain.SubmissionSource) *domain.Submission {
	at := time.UnixMilli(1759230005000).UTC()
	return &domain.Submission{
		GameID: f.game.ID, RoundID: f.round.ID, PlayerID: test.PlayerID, TestID: test.ID,
		Prompt: test.Prompt, Output: "out", PromptLength: 9, TestCount: 2, Source: source, SubmittedAt: at, LockedAt: at,
	}
}

func TestSubmissionRepository(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	subs := NewSubmissionRepository(db.New(f.pool))
	hostTest := f.completedTest(t, f.host, 1, "out")

	first := f.submission(hostTest, domain.SubmissionManual)
	created, err := subs.Create(ctx, first)
	if err != nil || !created || first.ID == uuid.Nil {
		t.Fatalf("Create() = %v, %v (id %s)", created, err, first.ID)
	}
	// A second submission for the same player and round writes nothing.
	second := f.submission(f.completedTest(t, f.host, 2, "other"), domain.SubmissionAuto)
	if created, err := subs.Create(ctx, second); err != nil || created {
		t.Fatalf("second Create() = %v, %v; want nothing written", created, err)
	}

	got, err := subs.GetForPlayer(ctx, f.round.ID, f.host)
	if err != nil || got.ID != first.ID || got.Source != domain.SubmissionManual || got.TestID != hostTest.ID ||
		got.PromptLength != 9 || got.TestCount != 2 || got.Prompt != hostTest.Prompt || !got.LockedAt.Equal(first.LockedAt) {
		t.Errorf("GetForPlayer() = %+v, %v", got, err)
	}
	if byID, err := subs.Get(ctx, first.ID); err != nil || byID.ID != first.ID {
		t.Errorf("Get() = %+v, %v", byID, err)
	}
	if _, err := subs.Get(ctx, uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Get(unknown) error = %v, want ErrNotFound", err)
	}
	if _, err := subs.GetForPlayer(ctx, f.round.ID, f.guest); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetForPlayer(guest) error = %v, want ErrNotFound", err)
	}
	if _, err := subs.Create(ctx, f.submission(f.completedTest(t, f.guest, 1, "g"), domain.SubmissionAuto)); err != nil {
		t.Fatal(err)
	}
	if n, _ := subs.Count(ctx, f.round.ID); n != 2 {
		t.Errorf("Count() = %d, want 2", n)
	}
	if list, _ := subs.ListByRound(ctx, f.round.ID); len(list) != 2 {
		t.Errorf("ListByRound() = %d, want 2", len(list))
	}
}

func TestSubmissionsAreImmutable(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	sub := f.submission(f.completedTest(t, f.host, 1, "out"), domain.SubmissionManual)
	if _, err := NewSubmissionRepository(db.New(f.pool)).Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	// The database refuses any update, whoever attempts it.
	_, err := f.pool.Exec(ctx, "UPDATE submissions SET prompt = 'changed' WHERE id = $1", sub.ID)
	if err == nil {
		t.Fatal("an UPDATE of a submission succeeded")
	}
	var prompt string
	_ = f.pool.QueryRow(ctx, "SELECT prompt FROM submissions WHERE id = $1", sub.ID).Scan(&prompt)
	if prompt != sub.Prompt {
		t.Errorf("prompt = %q after a refused update", prompt)
	}
}

func TestJudgingRepositoryClaims(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	runs := NewJudgingRepository(db.New(f.pool))
	now := time.UnixMilli(1759230100000).UTC()

	attempt, ok, err := runs.Claim(ctx, f.round.ID, f.game.ID, now, now.Add(-time.Minute))
	if err != nil || !ok || attempt != 1 {
		t.Fatalf("first Claim() = %d, %v, %v", attempt, ok, err)
	}
	if _, ok, _ := runs.Claim(ctx, f.round.ID, f.game.ID, now, now.Add(-time.Minute)); ok {
		t.Fatal("a running run was claimed twice")
	}
	if err := runs.Fail(ctx, f.round.ID, 1, now, "TIMEOUT"); err != nil {
		t.Fatal(err)
	}
	if run, _ := runs.Get(ctx, f.round.ID); run.Status != domain.JudgingFailed || run.ErrorCode != "TIMEOUT" {
		t.Errorf("run after Fail = %+v", run)
	}
	attempt, ok, _ = runs.Claim(ctx, f.round.ID, f.game.ID, now, now.Add(-time.Minute))
	if !ok || attempt != 2 {
		t.Fatalf("Claim() of a failed run = %d, %v; want attempt 2", attempt, ok)
	}
	// A stale running run can be taken over; the old worker then cannot finish.
	later := now.Add(10 * time.Minute)
	attempt, ok, _ = runs.Claim(ctx, f.round.ID, f.game.ID, later, later.Add(-time.Minute))
	if !ok || attempt != 3 {
		t.Fatalf("Claim() of a stale run = %d, %v; want attempt 3", attempt, ok)
	}
	if err := runs.Complete(ctx, f.round.ID, 2, later, domain.JudgeCall{}); !errors.Is(err, domain.ErrStaleGameState) {
		t.Errorf("Complete() by the superseded attempt error = %v, want ErrStaleGameState", err)
	}
	cost := 0.01
	if err := runs.Complete(ctx, f.round.ID, 3, later, domain.JudgeCall{Provider: "anthropic", Model: "m", InputTokens: 10, OutputTokens: 5, CostUSD: &cost}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := runs.Claim(ctx, f.round.ID, f.game.ID, later.Add(time.Hour), later.Add(time.Hour)); ok {
		t.Error("a completed run was claimed again")
	}
}

func TestScoresAreUniquePerPlayer(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	runs := NewJudgingRepository(db.New(f.pool))
	sub := f.submission(f.completedTest(t, f.host, 1, "out"), domain.SubmissionManual)
	if _, err := NewSubmissionRepository(db.New(f.pool)).Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	at := time.UnixMilli(1759230200000).UTC()
	host := domain.Score{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.host, SubmissionID: sub.ID, Label: "B",
		Components: domain.ScoreComponents{Accuracy: 800, Simplicity: 700, Creativity: 900, Adherence: 800, Efficiency: 1000},
		Total:      840, Rank: 1, Rationale: "good", Provider: "anthropic", Model: "m", CreatedAt: at}
	guest := domain.Score{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.guest, NoSubmission: true, Rank: 2, CreatedAt: at}
	for _, s := range []domain.Score{host, guest} {
		if err := runs.CreateScore(ctx, s); err != nil {
			t.Fatalf("CreateScore() error = %v", err)
		}
	}
	if err := runs.CreateScore(ctx, host); err == nil {
		t.Error("a second score for the same player was accepted")
	}
	scores, err := runs.ListScores(ctx, f.round.ID)
	if err != nil || len(scores) != 2 {
		t.Fatalf("ListScores() = %d, %v", len(scores), err)
	}
	if got := scores[0]; got.PlayerID != f.host || got.Total != 840 || got.Components != host.Components || got.Label != "B" || got.SubmissionID != sub.ID {
		t.Errorf("host score = %+v", got)
	}
	if got := scores[1]; !got.NoSubmission || got.SubmissionID != uuid.Nil || got.Label != "" {
		t.Errorf("guest score = %+v", got)
	}
}
