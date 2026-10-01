package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
)

// startedProgress is the progress of a game whose first round began at start.
func startedProgress(start time.Time) domain.GameProgress {
	return domain.GameProgress{
		Phase:          domain.GamePhaseRoundActive,
		CurrentRound:   1,
		TotalRounds:    testSettings.Rounds,
		RoundID:        uuid.New(),
		RoundStartedAt: start,
		RoundDeadline:  start.Add(90 * time.Second),
	}
}

func TestGameRepositoryProgressRoundTrip(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	g := f.game(t, f.user(t, "host"), "300001")

	lobby, err := f.repo.GetProgress(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetProgress() error = %v", err)
	}
	want := domain.GameProgress{Phase: domain.GamePhaseLobby, TotalRounds: testSettings.Rounds}
	if *lobby != want {
		t.Fatalf("new game progress = %+v, want %+v", *lobby, want)
	}

	start := time.UnixMilli(1759100000000).UTC()
	started := startedProgress(start)
	if err := f.repo.UpdateProgress(ctx, g.ID, *lobby, started); err != nil {
		t.Fatalf("UpdateProgress() error = %v", err)
	}

	got, err := f.repo.GetByID(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress != started {
		t.Errorf("stored progress = %+v, want %+v", got.Progress, started)
	}
	if got.Status != domain.GameStatusInProgress || got.StartedAt == nil {
		t.Errorf("status = %s, started_at = %v; want in_progress with a start time", got.Status, got.StartedAt)
	}

	// Timing is stored as Unix milliseconds, the same values clients get.
	var startedMs, deadlineMs int64
	err = f.pool.QueryRow(ctx, "SELECT round_started_at_ms, round_deadline_ms FROM games WHERE id = $1", g.ID).
		Scan(&startedMs, &deadlineMs)
	if err != nil {
		t.Fatal(err)
	}
	if startedMs != 1759100000000 || deadlineMs != 1759100090000 {
		t.Errorf("stored timing = %d..%d, want 1759100000000..1759100090000", startedMs, deadlineMs)
	}

	over := started
	over.Phase = domain.GamePhaseGameOver
	if err := f.repo.UpdateProgress(ctx, g.ID, started, over); err != nil {
		t.Fatalf("UpdateProgress(GameOver) error = %v", err)
	}
	if got, _ := f.repo.GetByID(ctx, g.ID); got.Status != domain.GameStatusFinished {
		t.Errorf("status = %s after GameOver, want finished", got.Status)
	}
}

func TestGameRepositoryUpdateProgressIsACompareAndSet(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	g := f.game(t, f.user(t, "host"), "300002")
	lobby := domain.GameProgress{Phase: domain.GamePhaseLobby, TotalRounds: testSettings.Rounds}
	started := startedProgress(time.UnixMilli(1759100000000).UTC())
	if err := f.repo.UpdateProgress(ctx, g.ID, lobby, started); err != nil {
		t.Fatal(err)
	}

	judging := started
	judging.Phase = domain.GamePhaseJudging

	t.Run("a stale phase writes nothing", func(t *testing.T) {
		if err := f.repo.UpdateProgress(ctx, g.ID, lobby, started); !errors.Is(err, domain.ErrStaleGameState) {
			t.Fatalf("UpdateProgress() from Lobby again error = %v, want ErrStaleGameState", err)
		}
	})

	t.Run("a stale round writes nothing", func(t *testing.T) {
		old := started
		old.RoundID = uuid.New()
		if err := f.repo.UpdateProgress(ctx, g.ID, old, judging); !errors.Is(err, domain.ErrStaleGameState) {
			t.Fatalf("UpdateProgress() for another round error = %v, want ErrStaleGameState", err)
		}
	})

	t.Run("a cancelled game writes nothing", func(t *testing.T) {
		other := f.game(t, f.user(t, "other"), "300003")
		if err := f.repo.UpdateStatus(ctx, other.ID, domain.GameStatusCancelled); err != nil {
			t.Fatal(err)
		}
		if err := f.repo.UpdateProgress(ctx, other.ID, lobby, started); !errors.Is(err, domain.ErrStaleGameState) {
			t.Fatalf("UpdateProgress() on a cancelled game error = %v, want ErrStaleGameState", err)
		}
	})

	t.Run("concurrent writers of the same change: exactly one wins", func(t *testing.T) {
		// Each writer uses its own connection, as two server instances would.
		const writers = 8
		var (
			wg   sync.WaitGroup
			mu   sync.Mutex
			wins int
		)
		for range writers {
			wg.Go(func() {
				err := NewGameRepository(db.New(f.pool)).UpdateProgress(ctx, g.ID, started, judging)
				switch {
				case err == nil:
					mu.Lock()
					wins++
					mu.Unlock()
				case !errors.Is(err, domain.ErrStaleGameState):
					t.Errorf("UpdateProgress() error = %v", err)
				}
			})
		}
		wg.Wait()
		if wins != 1 {
			t.Errorf("%d writers moved the round to Judging, want exactly 1", wins)
		}
	})
}

func TestGameRepositoryListByPhase(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	lobby := domain.GameProgress{Phase: domain.GamePhaseLobby, TotalRounds: testSettings.Rounds}

	active := f.game(t, host, "300004")
	started := startedProgress(time.UnixMilli(1759100000000).UTC())
	if err := f.repo.UpdateProgress(ctx, active.ID, lobby, started); err != nil {
		t.Fatal(err)
	}
	f.game(t, host, "300005") // still in the lobby

	games, err := f.repo.ListByPhase(ctx, domain.GamePhaseRoundActive)
	if err != nil {
		t.Fatalf("ListByPhase() error = %v", err)
	}
	if len(games) != 1 || games[0].ID != active.ID {
		t.Fatalf("ListByPhase(RoundActive) = %d games, want only %s", len(games), active.ID)
	}
	if games[0].Progress != started || games[0].Settings != testSettings {
		t.Errorf("listed game = %+v, want its progress and settings", games[0])
	}
}
