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
	"github.com/prompt-masters/backend/internal/repository"
)

func TestHelperBalances(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	repo := NewHelperRepository(db.New(f.pool))
	now := time.UnixMilli(1759300000000).UTC()

	if err := repo.EnsureBalance(ctx, f.game.ID, f.host, 100, now); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureBalance(ctx, f.game.ID, f.host, 999, now); err != nil {
		t.Fatal(err)
	}
	if b, _ := repo.GetBalance(ctx, f.game.ID, f.host); b != 100 {
		t.Fatalf("balance = %d, want 100: an existing balance is kept", b)
	}
	if _, err := repo.GetBalance(ctx, f.game.ID, f.guest); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing balance error = %v, want ErrNotFound", err)
	}

	if left, ok, err := repo.Debit(ctx, f.game.ID, f.host, 15, now); err != nil || !ok || left != 85 {
		t.Fatalf("Debit(15) = %d, %v, %v", left, ok, err)
	}
	if _, ok, _ := repo.Debit(ctx, f.game.ID, f.host, 86, now); ok {
		t.Error("a debit larger than the balance went through")
	}
	if b, _ := repo.Credit(ctx, f.game.ID, f.host, 15, now); b != 100 {
		t.Errorf("Credit() = %d, want 100", b)
	}
	if _, err := f.pool.Exec(ctx, "UPDATE helper_balances SET balance = -1 WHERE game_id = $1", f.game.ID); err == nil {
		t.Error("the database accepted a negative balance")
	}
}

// TestHelperDebitsNeverOverspend fires concurrent debits, each in its own
// transaction as concurrent requests would: exactly as many as the balance
// covers succeed.
func TestHelperDebitsNeverOverspend(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	now := time.UnixMilli(1759300000000).UTC()
	if err := NewHelperRepository(db.New(f.pool)).EnsureBalance(ctx, f.game.ID, f.host, 100, now); err != nil {
		t.Fatal(err)
	}
	tx := NewTransactor(f.pool)

	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
	)
	for range 20 {
		wg.Go(func() {
			err := tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
				_, debited, err := repos.Helpers.Debit(ctx, f.game.ID, f.host, 15, now)
				if debited {
					mu.Lock()
					ok++
					mu.Unlock()
				}
				return err
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	b, _ := NewHelperRepository(db.New(f.pool)).GetBalance(ctx, f.game.ID, f.host)
	if ok != 6 || b != 10 {
		t.Errorf("%d debits succeeded leaving %d, want 6 leaving 10", ok, b)
	}
}

func TestHelperUsageLifecycle(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	repo := NewHelperRepository(db.New(f.pool))
	now := time.UnixMilli(1759300000000).UTC()
	test := f.completedTest(t, f.host, 1, "out")

	u := &domain.HelperUsage{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.host, Type: domain.HelperReview, Cost: 15,
		IdempotencyKey: "key-1", InputPrompt: "draft", TestID: test.ID, CreatedAt: now}
	if created, err := repo.CreateUsage(ctx, u); err != nil || !created || u.ID == uuid.Nil {
		t.Fatalf("CreateUsage() = %v, %v", created, err)
	}
	dup := *u
	if created, err := repo.CreateUsage(ctx, &dup); err != nil || created {
		t.Fatalf("CreateUsage() with the same key = %v, %v; want nothing written", created, err)
	}
	// Another player may use the same key.
	other := &domain.HelperUsage{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.guest, Type: domain.HelperHint, Cost: 10, IdempotencyKey: "key-1", CreatedAt: now}
	if created, _ := repo.CreateUsage(ctx, other); !created {
		t.Error("another player's same key was refused")
	}

	got, err := repo.GetUsageByKey(ctx, f.host, "key-1")
	if err != nil || got.ID != u.ID || got.Status != domain.HelperReserved || got.TestID != test.ID || got.Cost != 15 || got.InputPrompt != "draft" {
		t.Fatalf("GetUsageByKey() = %+v, %v", got, err)
	}
	cost := 0.002
	if err := repo.Deliver(ctx, u.ID, domain.HelperDelivery{Content: "review", Provider: "anthropic", Model: "m", InputTokens: 50, OutputTokens: 20, CostUSD: &cost, DeliveredAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Deliver(ctx, u.ID, domain.HelperDelivery{Content: "again", DeliveredAt: now}); !errors.Is(err, domain.ErrStaleGameState) {
		t.Errorf("second Deliver() error = %v, want ErrStaleGameState", err)
	}
	if flipped, _ := repo.MarkRefunded(ctx, u.ID, "TIMEOUT", now); flipped {
		t.Error("a delivered usage was refunded")
	}

	list, _ := repo.ListUsage(ctx, f.round.ID, f.host)
	if len(list) != 1 || list[0].Status != domain.HelperDelivered || list[0].Content != "review" || *list[0].InputTokens != 50 || list[0].DeliveredAt == nil {
		t.Errorf("ListUsage() = %+v", list)
	}
	if stale, _ := repo.ListStaleReservations(ctx, now.Add(time.Hour)); len(stale) != 1 || stale[0].ID != other.ID {
		t.Errorf("stale reservations = %+v, want the guest's reserved one", stale)
	}
}

// TestHelperRefundsCreditOnce refunds one reservation from many transactions
// at once: the conditional status change lets exactly one credit the points.
func TestHelperRefundsCreditOnce(t *testing.T) {
	f := newRoundFixture(t)
	ctx := context.Background()
	now := time.UnixMilli(1759300000000).UTC()
	repo := NewHelperRepository(db.New(f.pool))
	if err := repo.EnsureBalance(ctx, f.game.ID, f.host, 85, now); err != nil {
		t.Fatal(err)
	}
	u := &domain.HelperUsage{GameID: f.game.ID, RoundID: f.round.ID, PlayerID: f.host, Type: domain.HelperReview, Cost: 15, IdempotencyKey: "k", CreatedAt: now}
	if _, err := repo.CreateUsage(ctx, u); err != nil {
		t.Fatal(err)
	}

	tx := NewTransactor(f.pool)
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			err := tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
				flipped, err := repos.Helpers.MarkRefunded(ctx, u.ID, "ABANDONED", now)
				if err != nil || !flipped {
					return err
				}
				_, err = repos.Helpers.Credit(ctx, f.game.ID, f.host, u.Cost, now)
				return err
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if b, _ := repo.GetBalance(ctx, f.game.ID, f.host); b != 100 {
		t.Errorf("balance = %d after concurrent refunds, want 100 (credited once)", b)
	}
}
