package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
	"github.com/prompt-masters/backend/internal/testutil/pgtest"
)

var testSettings = domain.GameSettings{
	Rounds:              5,
	TimePerRoundSeconds: 90,
	Difficulty:          domain.DifficultyMedium,
	Category:            domain.CategoryCoding,
	AIModel:             domain.AIModelClaudeSonnet5,
	MaxPlayers:          4,
}

type gameFixture struct {
	pool  *pgxpool.Pool
	repo  *GameRepository
	users *UserRepository
}

func newGameFixture(t *testing.T) *gameFixture {
	pool := pgtest.NewPool(t)
	queries := db.New(pool)
	return &gameFixture{pool: pool, repo: NewGameRepository(queries), users: NewUserRepository(queries)}
}

func (f *gameFixture) user(t *testing.T, name string) uuid.UUID {
	t.Helper()
	u, err := f.users.Create(context.Background(), &domain.User{
		Username: name,
		Email:    name + "@example.com",
		Password: "hash",
	})
	if err != nil {
		t.Fatalf("creating user %q: %v", name, err)
	}
	return u.ID
}

// game creates a waiting game with settings and the host as its first player.
func (f *gameFixture) game(t *testing.T, hostID uuid.UUID, code string) *domain.Game {
	t.Helper()
	ctx := context.Background()
	g, err := f.repo.Create(ctx, hostID, code)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", code, err)
	}
	if err := f.repo.CreateSettings(ctx, g.ID, testSettings); err != nil {
		t.Fatalf("CreateSettings() error = %v", err)
	}
	if err := f.repo.AddPlayer(ctx, g.ID, hostID); err != nil {
		t.Fatalf("AddPlayer(host) error = %v", err)
	}
	return g
}

func TestGameRepositoryCreateAndGetByID(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	guest := f.user(t, "guest")

	created := f.game(t, host, "000123")
	if err := f.repo.AddPlayer(ctx, created.ID, guest); err != nil {
		t.Fatalf("AddPlayer(guest) error = %v", err)
	}

	got, err := f.repo.GetByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}

	if got.RoomCode != "000123" {
		t.Errorf("RoomCode = %q, want leading zeros preserved as %q", got.RoomCode, "000123")
	}
	if got.HostID != host || got.Status != domain.GameStatusWaiting || got.StartedAt != nil {
		t.Errorf("game = %+v, want waiting game hosted by %s", got, host)
	}
	if got.Settings != testSettings {
		t.Errorf("Settings = %+v, want %+v", got.Settings, testSettings)
	}
	if got.PlayerCount != 2 || len(got.Players) != 2 {
		t.Fatalf("PlayerCount = %d, players = %d, want 2", got.PlayerCount, len(got.Players))
	}
	if got.Players[0].UserID != host || got.Players[0].Username != "host" || got.Players[1].UserID != guest {
		t.Errorf("Players = %+v, want host then guest", got.Players)
	}
}

func TestGameRepositoryGetByIDNotFound(t *testing.T) {
	f := newGameFixture(t)

	if _, err := f.repo.GetByID(context.Background(), uuid.New()); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetByID() error = %v, want domain.ErrNotFound", err)
	}
	if _, err := f.repo.GetActiveIDByRoomCode(context.Background(), "999999"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("GetActiveIDByRoomCode() error = %v, want domain.ErrNotFound", err)
	}
}

func TestGameRepositoryRoomCodeUniqueAmongActiveGames(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")

	first := f.game(t, host, "424242")

	if _, err := f.repo.Create(ctx, host, "424242"); !errors.Is(err, domain.ErrRoomCodeTaken) {
		t.Fatalf("Create() with an active code error = %v, want domain.ErrRoomCodeTaken", err)
	}

	id, err := f.repo.GetActiveIDByRoomCode(ctx, "424242")
	if err != nil || id != first.ID {
		t.Fatalf("GetActiveIDByRoomCode() = %s, %v; want %s", id, err, first.ID)
	}

	if err := f.repo.UpdateStatus(ctx, first.ID, domain.GameStatusCancelled); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}
	if _, err := f.repo.GetActiveIDByRoomCode(ctx, "424242"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("cancelled game still resolves by room code: %v", err)
	}

	second, err := f.repo.Create(ctx, host, "424242")
	if err != nil {
		t.Fatalf("Create() reusing a cancelled game's code error = %v", err)
	}
	if second.ID == first.ID {
		t.Error("Create() returned the cancelled game")
	}
}

func TestGameRepositoryRoomCodeCollisionKeepsTransactionUsable(t *testing.T) {
	f := newGameFixture(t)
	host := f.user(t, "host")
	f.game(t, host, "111111")

	err := NewTransactor(f.pool).WithinTx(context.Background(), func(repos repository.TxRepositories) error {
		games := repos.Games
		if _, err := games.Create(context.Background(), host, "111111"); !errors.Is(err, domain.ErrRoomCodeTaken) {
			return fmt.Errorf("colliding Create() error = %v, want ErrRoomCodeTaken", err)
		}
		_, err := games.Create(context.Background(), host, "222222")
		return err
	})
	if err != nil {
		t.Fatalf("retrying inside the transaction failed: %v", err)
	}
}

func TestGameRepositoryPlayers(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	guest := f.user(t, "guest")
	g := f.game(t, host, "333333")

	tests := []struct {
		name    string
		run     func() error
		wantErr error
	}{
		{name: "host cannot be added twice", run: func() error { return f.repo.AddPlayer(ctx, g.ID, host) }, wantErr: domain.ErrAlreadyJoined},
		{name: "guest joins", run: func() error { return f.repo.AddPlayer(ctx, g.ID, guest) }},
		{name: "guest cannot join twice", run: func() error { return f.repo.AddPlayer(ctx, g.ID, guest) }, wantErr: domain.ErrAlreadyJoined},
		{name: "guest leaves", run: func() error { return f.repo.RemovePlayer(ctx, g.ID, guest) }},
		{name: "guest cannot leave twice", run: func() error { return f.repo.RemovePlayer(ctx, g.ID, guest) }, wantErr: domain.ErrNotInGame},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestGameRepositoryUpdates(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	guest := f.user(t, "guest")
	g := f.game(t, host, "444444")

	updated := domain.GameSettings{
		Rounds:              7,
		TimePerRoundSeconds: 120,
		Difficulty:          domain.DifficultyHard,
		Category:            domain.CategoryReasoning,
		AIModel:             domain.AIModelClaudeOpus5,
		MaxPlayers:          6,
	}
	if err := f.repo.UpdateSettings(ctx, g.ID, updated); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	if err := f.repo.UpdateHost(ctx, g.ID, guest); err != nil {
		t.Fatalf("UpdateHost() error = %v", err)
	}
	if err := f.repo.UpdateStatus(ctx, g.ID, domain.GameStatusInProgress); err != nil {
		t.Fatalf("UpdateStatus() error = %v", err)
	}

	got, err := f.repo.GetByID(ctx, g.ID)
	if err != nil {
		t.Fatalf("GetByID() error = %v", err)
	}
	if got.Settings != updated {
		t.Errorf("Settings = %+v, want %+v", got.Settings, updated)
	}
	if got.HostID != guest {
		t.Errorf("HostID = %s, want %s", got.HostID, guest)
	}
	if got.Status != domain.GameStatusInProgress || got.StartedAt == nil {
		t.Errorf("Status = %s, StartedAt = %v; want in_progress with a start time", got.Status, got.StartedAt)
	}

	// Moving on from in_progress keeps the original start time.
	if err := f.repo.UpdateStatus(ctx, g.ID, domain.GameStatusCancelled); err != nil {
		t.Fatalf("UpdateStatus(cancelled) error = %v", err)
	}
	cancelled, _ := f.repo.GetByID(ctx, g.ID)
	if cancelled.StartedAt == nil || !cancelled.StartedAt.Equal(*got.StartedAt) {
		t.Errorf("StartedAt = %v after cancelling, want %v", cancelled.StartedAt, got.StartedAt)
	}
}

func TestGameRepositoryRejectsInvalidSettings(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	g, err := f.repo.Create(ctx, f.user(t, "host"), "555555")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	bad := testSettings
	bad.MaxPlayers = 7
	if err := f.repo.CreateSettings(ctx, g.ID, bad); err == nil {
		t.Fatal("CreateSettings() with max_players 7 error = nil, want a CHECK violation")
	}
}

func TestGameRepositoryListByStatus(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	guest := f.user(t, "guest")

	older := f.game(t, host, "600001")
	newer := f.game(t, host, "600002")
	started := f.game(t, host, "600003")
	if err := f.repo.AddPlayer(ctx, older.ID, guest); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.UpdateStatus(ctx, started.ID, domain.GameStatusInProgress); err != nil {
		t.Fatal(err)
	}

	waiting, err := f.repo.ListByStatus(ctx, domain.GameStatusWaiting, 10)
	if err != nil {
		t.Fatalf("ListByStatus() error = %v", err)
	}
	if len(waiting) != 2 || waiting[0].ID != newer.ID || waiting[1].ID != older.ID {
		t.Fatalf("ListByStatus(waiting) = %v, want [newer, older]", waiting)
	}
	if waiting[1].PlayerCount != 2 || waiting[1].Settings != testSettings {
		t.Errorf("older game = %+v, want 2 players and test settings", waiting[1])
	}

	limited, err := f.repo.ListByStatus(ctx, domain.GameStatusWaiting, 1)
	if err != nil || len(limited) != 1 {
		t.Errorf("ListByStatus(limit 1) = %d games, %v; want 1", len(limited), err)
	}
}

func TestGameTransactorRollsBackOnError(t *testing.T) {
	f := newGameFixture(t)
	ctx := context.Background()
	host := f.user(t, "host")
	wantErr := errors.New("abort")

	err := NewTransactor(f.pool).WithinTx(ctx, func(repos repository.TxRepositories) error {
		games := repos.Games
		if _, err := games.Create(ctx, host, "777777"); err != nil {
			return err
		}
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("WithinTx() error = %v, want %v", err, wantErr)
	}
	if _, err := f.repo.GetActiveIDByRoomCode(ctx, "777777"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("game survived the rollback: %v", err)
	}
}

func TestGameRepositoryLockByIDBlocksConcurrentLockers(t *testing.T) {
	f := newGameFixture(t)
	g := f.game(t, f.user(t, "host"), "888888")
	tx := NewTransactor(f.pool)

	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- tx.WithinTx(context.Background(), func(repos repository.TxRepositories) error {
			games := repos.Games
			if _, err := games.LockByID(context.Background(), g.ID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		games := repos.Games
		_, err := games.LockByID(ctx, g.ID)
		return err
	})
	if err == nil {
		t.Error("second LockByID() succeeded while the first transaction held the lock")
	}

	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("lock holder error = %v", err)
	}

	err = tx.WithinTx(context.Background(), func(repos repository.TxRepositories) error {
		games := repos.Games
		_, err := games.LockByID(context.Background(), g.ID)
		return err
	})
	if err != nil {
		t.Errorf("LockByID() after release error = %v", err)
	}
}

// A transaction that waits for the lock must see what the previous holder
// committed; a stale player count here is what lets concurrent joins overfill
// a room.
func TestGameRepositoryLockByIDSeesChangesCommittedWhileWaiting(t *testing.T) {
	f := newGameFixture(t)
	host, guest := f.user(t, "host"), f.user(t, "guest")
	g := f.game(t, host, "999999")
	tx := NewTransactor(f.pool)

	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- tx.WithinTx(context.Background(), func(repos repository.TxRepositories) error {
			games := repos.Games
			if _, err := games.LockByID(context.Background(), g.ID); err != nil {
				return err
			}
			if err := games.AddPlayer(context.Background(), g.ID, guest); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	waiterDone := make(chan *domain.Game, 1)
	go func() {
		_ = tx.WithinTx(context.Background(), func(repos repository.TxRepositories) error {
			games := repos.Games
			got, err := games.LockByID(context.Background(), g.ID)
			if err != nil {
				t.Errorf("waiting LockByID() error = %v", err)
			}
			waiterDone <- got
			return err
		})
	}()

	// Give the waiter time to block on the lock before the holder commits.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatalf("lock holder error = %v", err)
	}

	got := <-waiterDone
	if got == nil {
		t.Fatal("waiting LockByID() returned no game")
	}
	if got.PlayerCount != 2 || len(got.Players) != 2 {
		t.Errorf("after waiting: PlayerCount = %d, players = %d; want 2 (the committed join)", got.PlayerCount, len(got.Players))
	}
}
