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

// roundFixture is a game with a recorded round and two players.
type roundFixture struct {
	*gameFixture
	tests       *PromptTestRepository
	host, guest uuid.UUID
	game        *domain.Game
	round       domain.Round
}

func newRoundFixture(t *testing.T) *roundFixture {
	t.Helper()
	f := &roundFixture{gameFixture: newGameFixture(t)}
	f.tests = NewPromptTestRepository(db.New(f.pool))
	f.host, f.guest = f.user(t, "host"), f.user(t, "guest")
	f.game = f.newGame(t)
	start := time.UnixMilli(1759230000000).UTC()
	f.round = domain.Round{ID: uuid.New(), GameID: f.game.ID, Number: 1, StartedAt: start, Deadline: start.Add(90 * time.Second)}
	if err := f.repo.CreateRound(context.Background(), f.round); err != nil {
		t.Fatalf("CreateRound() error = %v", err)
	}
	return f
}

func (f *roundFixture) newGame(t *testing.T) *domain.Game {
	t.Helper()
	g := f.gameFixture.game(t, f.host, "400001")
	if err := f.repo.AddPlayer(context.Background(), g.ID, f.guest); err != nil {
		t.Fatal(err)
	}
	return g
}

func (f *roundFixture) create(t *testing.T, player uuid.UUID, number int, at time.Time) *domain.PromptTest {
	t.Helper()
	test := &domain.PromptTest{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: player, TestNumber: number, Prompt: "p  \n exact", CreatedAt: at}
	if err := f.tests.Create(context.Background(), test); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	return test
}

func TestGameRoundsRoundTrip(t *testing.T) {
	f := newRoundFixture(t)
	got, err := f.repo.GetRound(context.Background(), f.round.ID)
	if err != nil || *got != f.round {
		t.Fatalf("GetRound() = %+v, %v; want %+v", got, err, f.round)
	}
	if _, err := f.repo.GetRound(context.Background(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown round error = %v, want ErrNotFound", err)
	}
	dup := f.round
	dup.ID = uuid.New()
	if err := f.repo.CreateRound(context.Background(), dup); err == nil {
		t.Error("a second round 1 for the same game was accepted")
	}
}

func TestPromptTestNumberingAndUniqueness(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	now := time.UnixMilli(1759230001000).UTC()

	for want := 1; want <= 3; want++ {
		n, err := f.tests.NextNumber(ctx, f.round.ID, f.host)
		if err != nil || n != want {
			t.Fatalf("NextNumber() = %d, %v; want %d", n, err, want)
		}
		f.create(t, f.host, n, now)
	}
	if n, _ := f.tests.NextNumber(ctx, f.round.ID, f.guest); n != 1 {
		t.Errorf("guest's first number = %d, want 1: numbering is per player", n)
	}
	dup := &domain.PromptTest{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.host, TestNumber: 2, Prompt: "p", CreatedAt: now}
	if err := f.tests.Create(ctx, dup); err == nil {
		t.Error("a duplicate test number was accepted")
	}
}

func TestPromptTestLifecycle(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	created := time.UnixMilli(1759230001000).UTC()
	test := f.create(t, f.guest, 1, created)
	if test.ID == uuid.Nil {
		t.Fatal("Create() did not set the ID")
	}

	if n, _ := f.tests.CountUnfinished(ctx, f.round.ID, f.guest, created.Add(-time.Minute)); n != 1 {
		t.Errorf("unfinished = %d, want 1", n)
	}
	if n, _ := f.tests.CountUnfinished(ctx, f.round.ID, f.guest, created.Add(time.Second)); n != 0 {
		t.Errorf("unfinished since after its creation = %d, want 0: stale tests do not count", n)
	}

	if err := f.tests.MarkStreaming(ctx, test.ID, created.Add(10*time.Millisecond)); err != nil {
		t.Fatalf("MarkStreaming() error = %v", err)
	}
	if err := f.tests.MarkStreaming(ctx, test.ID, created); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second MarkStreaming() error = %v, want ErrNotFound", err)
	}

	out, in, outTokens, cost, latency := "Hello", 12, 34, 0.0004, int64(2100)
	res := domain.PromptTestResult{
		Status: domain.PromptTestCompleted, Output: &out, Provider: "anthropic", Model: "claude-sonnet-5",
		InputTokens: &in, OutputTokens: &outTokens, CostUSD: &cost, LatencyMs: &latency, CompletedAt: created.Add(2100 * time.Millisecond),
	}
	if err := f.tests.Finish(ctx, test.ID, res); err != nil {
		t.Fatalf("Finish() error = %v", err)
	}
	if err := f.tests.Finish(ctx, test.ID, domain.PromptTestResult{Status: domain.PromptTestFailed, CompletedAt: created}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("Finish() of a finished test error = %v, want ErrNotFound", err)
	}

	list, err := f.tests.ListByRoundAndPlayer(ctx, f.round.ID, f.guest)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByRoundAndPlayer() = %d, %v", len(list), err)
	}
	got := list[0]
	if got.Status != domain.PromptTestCompleted || *got.Output != "Hello" || got.Prompt != "p  \n exact" || got.ErrorCode != "" ||
		*got.InputTokens != 12 || *got.OutputTokens != 34 || *got.CostUSD != 0.0004 || *got.LatencyMs != 2100 ||
		got.Provider != "anthropic" || got.Model != "claude-sonnet-5" || !got.CreatedAt.Equal(created) ||
		got.StartedAt == nil || got.CompletedAt == nil || got.CompletedAt.UnixMilli() != 1759230003100 {
		t.Errorf("stored test = %+v", got)
	}
	if hosts, _ := f.tests.ListByRoundAndPlayer(ctx, f.round.ID, f.host); len(hosts) != 0 {
		t.Errorf("host sees %d of the guest's tests", len(hosts))
	}
}

func TestPromptTestFailStale(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	now := time.UnixMilli(1759230600000).UTC()
	stale := f.create(t, f.host, 1, now.Add(-time.Hour))
	fresh := f.create(t, f.guest, 1, now)

	n, err := f.tests.FailStale(ctx, now.Add(-5*time.Minute), now)
	if err != nil || n != 1 {
		t.Fatalf("FailStale() = %d, %v; want 1", n, err)
	}
	hosts, _ := f.tests.ListByRoundAndPlayer(ctx, f.round.ID, f.host)
	if hosts[0].ID != stale.ID || hosts[0].Status != domain.PromptTestFailed || hosts[0].ErrorCode != "INTERRUPTED" || hosts[0].Output != nil {
		t.Errorf("stale test = %+v", hosts[0])
	}
	guests, _ := f.tests.ListByRoundAndPlayer(ctx, f.round.ID, f.guest)
	if guests[0].ID != fresh.ID || guests[0].Status != domain.PromptTestPending {
		t.Errorf("fresh test = %+v, want it untouched", guests[0])
	}
}
