package redisstore

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/testutil/redistest"
	"github.com/redis/go-redis/v9"
)

const (
	testActiveTTL = 2 * time.Hour
	testEndedTTL  = 15 * time.Minute
)

type storeFixture struct {
	store  *Store
	client *redis.Client
	prefix string
}

func newStoreFixture(t *testing.T) *storeFixture {
	t.Helper()
	client, prefix := redistest.Client(t)
	store, err := New(client, Options{KeyPrefix: prefix, ActiveTTL: testActiveTTL, EndedTTL: testEndedTTL, OpTimeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return &storeFixture{store: store, client: client, prefix: prefix}
}

var baseTime = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

func lobbyFor(gameID, hostID uuid.UUID, status domain.GameStatus) domain.LobbyState {
	return domain.LobbyState{
		GameID:   gameID,
		RoomCode: "004213",
		HostID:   hostID,
		Status:   status,
		Settings: domain.GameSettings{
			Rounds:              5,
			TimePerRoundSeconds: 90,
			Difficulty:          domain.DifficultyMedium,
			Category:            domain.CategoryCoding,
			AIModel:             domain.AIModelClaudeSonnet5,
			MaxPlayers:          4,
		},
		UpdatedAt: baseTime,
	}
}

func player(id uuid.UUID, name string, joinedAfter time.Duration) domain.LivePlayer {
	return domain.LivePlayer{UserID: id, Username: name, JoinedAt: baseTime.Add(joinedAfter)}
}

// seedGame writes a lobby with the given players, the first being host.
func (f *storeFixture) seedGame(t *testing.T, gameID uuid.UUID, players ...domain.LivePlayer) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.SetLobby(ctx, lobbyFor(gameID, players[0].UserID, domain.GameStatusWaiting)); err != nil {
		t.Fatalf("SetLobby() error = %v", err)
	}
	for _, p := range players {
		if err := f.store.AddPlayer(ctx, gameID, p); err != nil {
			t.Fatalf("AddPlayer() error = %v", err)
		}
	}
}

// assertAllKeysExpire checks that every key under the test prefix has an
// expiry within (0, max].
func (f *storeFixture) assertAllKeysExpire(t *testing.T, max time.Duration) {
	t.Helper()
	keys := redistest.Keys(t, f.client, f.prefix)
	if len(keys) == 0 {
		t.Fatal("no keys to check")
	}
	for _, k := range keys {
		ttl, err := f.client.PTTL(context.Background(), k).Result()
		if err != nil {
			t.Fatalf("PTTL(%s) error = %v", k, err)
		}
		if ttl <= 0 || ttl > max {
			t.Errorf("key %s has TTL %s, want within (0, %s]", k, ttl, max)
		}
	}
}

func TestStoreLobby(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID, host := uuid.New(), uuid.New()

	if _, err := f.store.GetLobby(ctx, gameID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Fatalf("GetLobby() before writing error = %v, want ErrStateNotFound", err)
	}

	want := lobbyFor(gameID, host, domain.GameStatusWaiting)
	if err := f.store.SetLobby(ctx, want); err != nil {
		t.Fatalf("SetLobby() error = %v", err)
	}
	got, err := f.store.GetLobby(ctx, gameID)
	if err != nil {
		t.Fatalf("GetLobby() error = %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetLobby() = %+v, want %+v", *got, want)
	}

	if err := f.store.SetLobby(ctx, domain.LobbyState{GameID: gameID, Status: "paused", HostID: host}); err == nil {
		t.Error("SetLobby() with an invalid status error = nil, want rejection")
	}
}

func TestStorePlayers(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	host, second, third := player(uuid.New(), "host", 0), player(uuid.New(), "second", time.Second), player(uuid.New(), "third", 2*time.Second)

	// Added out of order; listed by join time.
	for _, p := range []domain.LivePlayer{third, host, second} {
		if err := f.store.AddPlayer(ctx, gameID, p); err != nil {
			t.Fatalf("AddPlayer() error = %v", err)
		}
	}
	got, err := f.store.ListPlayers(ctx, gameID)
	if err != nil {
		t.Fatalf("ListPlayers() error = %v", err)
	}
	if want := []domain.LivePlayer{host, second, third}; !reflect.DeepEqual(got, want) {
		t.Errorf("ListPlayers() = %+v, want %+v", got, want)
	}

	// Removing a player also clears their readiness and draft.
	if err := f.store.SetReady(ctx, gameID, second.UserID); err != nil {
		t.Fatalf("SetReady() error = %v", err)
	}
	if err := f.store.SaveDraft(ctx, gameID, second.UserID, domain.Draft{Content: "draft", RoundNumber: 1, UpdatedAt: baseTime}); err != nil {
		t.Fatalf("SaveDraft() error = %v", err)
	}
	if err := f.store.RemovePlayer(ctx, gameID, second.UserID); err != nil {
		t.Fatalf("RemovePlayer() error = %v", err)
	}

	got, _ = f.store.ListPlayers(ctx, gameID)
	if want := []domain.LivePlayer{host, third}; !reflect.DeepEqual(got, want) {
		t.Errorf("after removal ListPlayers() = %+v, want %+v", got, want)
	}
	if ready, _ := f.store.ListReady(ctx, gameID); len(ready) != 0 {
		t.Errorf("removed player still ready: %v", ready)
	}
	if _, err := f.store.GetDraft(ctx, gameID, second.UserID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Errorf("removed player's draft still readable: %v", err)
	}

	// An empty roster is an empty list, not an error.
	empty, err := f.store.ListPlayers(ctx, uuid.New())
	if err != nil || len(empty) != 0 {
		t.Errorf("ListPlayers() on an unknown game = %v, %v; want empty", empty, err)
	}
}

func TestStoreSyncLobbyReplacesRoster(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	host, stays, leaves := player(uuid.New(), "host", 0), player(uuid.New(), "stays", time.Second), player(uuid.New(), "leaves", 2*time.Second)
	f.seedGame(t, gameID, host, stays, leaves)

	if err := f.store.SetReady(ctx, gameID, leaves.UserID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SaveDraft(ctx, gameID, leaves.UserID, domain.Draft{Content: "x", UpdatedAt: baseTime}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.IncrementPoints(ctx, gameID, leaves.UserID, 7); err != nil {
		t.Fatal(err)
	}

	lobby := lobbyFor(gameID, stays.UserID, domain.GameStatusWaiting)
	joiner := player(uuid.New(), "joiner", 3*time.Second)
	if err := f.store.SyncLobby(ctx, lobby, []domain.LivePlayer{stays, joiner}); err != nil {
		t.Fatalf("SyncLobby() error = %v", err)
	}

	gotLobby, _ := f.store.GetLobby(ctx, gameID)
	if gotLobby.HostID != stays.UserID {
		t.Errorf("host = %s, want %s", gotLobby.HostID, stays.UserID)
	}
	players, _ := f.store.ListPlayers(ctx, gameID)
	if want := []domain.LivePlayer{stays, joiner}; !reflect.DeepEqual(players, want) {
		t.Errorf("players = %+v, want %+v", players, want)
	}
	if ready, _ := f.store.ListReady(ctx, gameID); len(ready) != 0 {
		t.Errorf("dropped player still ready: %v", ready)
	}
	if _, err := f.store.GetDraft(ctx, gameID, leaves.UserID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Errorf("dropped player's draft still readable: %v", err)
	}
	if points, _ := f.store.GetPoints(ctx, gameID); points[leaves.UserID] != 7 {
		t.Errorf("points = %v, want the dropped player's points kept", points)
	}
}

func TestStoreReady(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)
	f.seedGame(t, gameID, a, b)

	for _, id := range []uuid.UUID{a.UserID, b.UserID, a.UserID} {
		if err := f.store.SetReady(ctx, gameID, id); err != nil {
			t.Fatalf("SetReady() error = %v", err)
		}
	}
	ready, err := f.store.ListReady(ctx, gameID)
	if err != nil || len(ready) != 2 {
		t.Fatalf("ListReady() = %v, %v; want both players once", ready, err)
	}

	if err := f.store.UnsetReady(ctx, gameID, a.UserID); err != nil {
		t.Fatalf("UnsetReady() error = %v", err)
	}
	ready, _ = f.store.ListReady(ctx, gameID)
	if !reflect.DeepEqual(ready, []uuid.UUID{b.UserID}) {
		t.Errorf("ListReady() = %v, want only b", ready)
	}

	outsider := uuid.New()
	if err := f.store.SetReady(ctx, gameID, outsider); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("SetReady(outsider) error = %v, want ErrNotGamePlayer", err)
	}
	if err := f.store.UnsetReady(ctx, gameID, outsider); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("UnsetReady(outsider) error = %v, want ErrNotGamePlayer", err)
	}
}

func TestStoreRound(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()

	if _, err := f.store.GetRound(ctx, gameID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Fatalf("GetRound() before writing error = %v, want ErrStateNotFound", err)
	}

	want := domain.RoundState{Number: 2, ChallengeID: uuid.New(), StartedAt: baseTime, Deadline: baseTime.Add(90 * time.Second)}
	if err := f.store.SetRound(ctx, gameID, want); err != nil {
		t.Fatalf("SetRound() error = %v", err)
	}
	got, err := f.store.GetRound(ctx, gameID)
	if err != nil {
		t.Fatalf("GetRound() error = %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetRound() = %+v, want %+v", *got, want)
	}

	bad := want
	bad.Deadline = bad.StartedAt
	if err := f.store.SetRound(ctx, gameID, bad); err == nil {
		t.Error("SetRound() with a deadline not after the start error = nil, want rejection")
	}
}

func TestStoreDrafts(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)
	f.seedGame(t, gameID, a, b)

	want := domain.Draft{Content: "Write a haiku about {autumn} \"quoted\" ✓", RoundNumber: 1, UpdatedAt: baseTime}
	if err := f.store.SaveDraft(ctx, gameID, a.UserID, want); err != nil {
		t.Fatalf("SaveDraft() error = %v", err)
	}
	got, err := f.store.GetDraft(ctx, gameID, a.UserID)
	if err != nil {
		t.Fatalf("GetDraft() error = %v", err)
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("GetDraft() = %+v, want %+v", *got, want)
	}

	// Each player's draft lives under their own key.
	if _, err := f.store.GetDraft(ctx, gameID, b.UserID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Errorf("GetDraft(b) error = %v, want ErrStateNotFound", err)
	}
	if err := f.store.SaveDraft(ctx, gameID, uuid.New(), want); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("SaveDraft(outsider) error = %v, want ErrNotGamePlayer", err)
	}
}

func TestStorePoints(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)
	f.seedGame(t, gameID, a, b)

	steps := []struct {
		user  uuid.UUID
		delta int64
		want  int64
	}{
		{a.UserID, 10, 10},
		{a.UserID, 5, 15},
		{b.UserID, 3, 3},
		{a.UserID, -4, 11},
	}
	for _, s := range steps {
		got, err := f.store.IncrementPoints(ctx, gameID, s.user, s.delta)
		if err != nil || got != s.want {
			t.Fatalf("IncrementPoints(%+d) = %d, %v; want %d", s.delta, got, err, s.want)
		}
	}
	points, err := f.store.GetPoints(ctx, gameID)
	if err != nil {
		t.Fatalf("GetPoints() error = %v", err)
	}
	if want := map[uuid.UUID]int64{a.UserID: 11, b.UserID: 3}; !reflect.DeepEqual(points, want) {
		t.Errorf("GetPoints() = %v, want %v", points, want)
	}
	if _, err := f.store.IncrementPoints(ctx, gameID, uuid.New(), 1); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("IncrementPoints(outsider) error = %v, want ErrNotGamePlayer", err)
	}
}

func TestStorePointsConcurrentIncrementsAreAtomic(t *testing.T) {
	f := newStoreFixture(t)
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)
	f.seedGame(t, gameID, a, b)

	const workers, perWorker = 40, 25
	var wg sync.WaitGroup
	for i := range workers {
		user := a.UserID
		if i%2 == 1 {
			user = b.UserID
		}
		wg.Go(func() {
			for range perWorker {
				if _, err := f.store.IncrementPoints(context.Background(), gameID, user, 1); err != nil {
					t.Errorf("IncrementPoints() error = %v", err)
					return
				}
			}
		})
	}
	wg.Wait()

	points, err := f.store.GetPoints(context.Background(), gameID)
	if err != nil {
		t.Fatalf("GetPoints() error = %v", err)
	}
	want := int64(workers / 2 * perWorker)
	if points[a.UserID] != want || points[b.UserID] != want {
		t.Errorf("points = %v, want %d each", points, want)
	}
}

func TestStoreLoad(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)

	if _, err := f.store.Load(ctx, gameID, a.UserID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Fatalf("Load() without state error = %v, want ErrStateNotFound", err)
	}

	f.seedGame(t, gameID, a, b)
	round := domain.RoundState{Number: 1, ChallengeID: uuid.New(), StartedAt: baseTime, Deadline: baseTime.Add(time.Minute)}
	draftA := domain.Draft{Content: "a's secret prompt", RoundNumber: 1, UpdatedAt: baseTime}
	draftB := domain.Draft{Content: "b's secret prompt", RoundNumber: 1, UpdatedAt: baseTime}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.store.SetReady(ctx, gameID, b.UserID))
	must(f.store.SetRound(ctx, gameID, round))
	must(f.store.SaveDraft(ctx, gameID, a.UserID, draftA))
	must(f.store.SaveDraft(ctx, gameID, b.UserID, draftB))
	_, err := f.store.IncrementPoints(ctx, gameID, a.UserID, 12)
	must(err)

	got, err := f.store.Load(ctx, gameID, a.UserID)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.Lobby.HostID != a.UserID || got.Lobby.Status != domain.GameStatusWaiting {
		t.Errorf("Lobby = %+v", got.Lobby)
	}
	if !reflect.DeepEqual(got.Players, []domain.LivePlayer{a, b}) {
		t.Errorf("Players = %+v", got.Players)
	}
	if !reflect.DeepEqual(got.Ready, []uuid.UUID{b.UserID}) {
		t.Errorf("Ready = %v, want [b]", got.Ready)
	}
	if got.Round == nil || !reflect.DeepEqual(*got.Round, round) {
		t.Errorf("Round = %+v, want %+v", got.Round, round)
	}
	if got.Draft == nil || *got.Draft != draftA {
		t.Errorf("Draft = %+v, want only a's own draft", got.Draft)
	}
	if got.Points[a.UserID] != 12 {
		t.Errorf("Points = %v", got.Points)
	}

	// b sees b's draft, never a's.
	gotB, err := f.store.Load(ctx, gameID, b.UserID)
	if err != nil || gotB.Draft == nil || *gotB.Draft != draftB {
		t.Errorf("Load(b).Draft = %+v, %v; want b's own draft", gotB.Draft, err)
	}

	// A player without a draft or round still loads.
	must(f.store.Delete(ctx, gameID))
	f.seedGame(t, gameID, a)
	minimal, err := f.store.Load(ctx, gameID, a.UserID)
	if err != nil {
		t.Fatalf("Load() of a minimal game error = %v", err)
	}
	if minimal.Draft != nil || minimal.Round != nil || len(minimal.Ready) != 0 || len(minimal.Points) != 0 {
		t.Errorf("minimal Load() = %+v, want no draft, round, ready or points", minimal)
	}
}

func TestStoreRoomsAreIsolated(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	roomA, roomB := uuid.New(), uuid.New()
	alice, bob := player(uuid.New(), "alice", 0), player(uuid.New(), "bob", 0)
	f.seedGame(t, roomA, alice)
	f.seedGame(t, roomB, bob)

	if err := f.store.SaveDraft(ctx, roomA, alice.UserID, domain.Draft{Content: "room A secret", UpdatedAt: baseTime}); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetReady(ctx, roomA, alice.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.IncrementPoints(ctx, roomA, alice.UserID, 50); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetRound(ctx, roomA, domain.RoundState{Number: 3, ChallengeID: uuid.New(), StartedAt: baseTime, Deadline: baseTime.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}

	// Nothing from room A is visible through room B.
	if _, err := f.store.GetDraft(ctx, roomB, alice.UserID); !errors.Is(err, domain.ErrStateNotFound) {
		t.Errorf("room A draft readable via room B: %v", err)
	}
	b, err := f.store.Load(ctx, roomB, alice.UserID)
	if err != nil {
		t.Fatalf("Load(room B) error = %v", err)
	}
	if b.Draft != nil || b.Round != nil || len(b.Ready) != 0 || len(b.Points) != 0 {
		t.Errorf("room B state = %+v, leaked room A data", b)
	}
	if len(b.Players) != 1 || b.Players[0].UserID != bob.UserID || b.Lobby.HostID != bob.UserID {
		t.Errorf("room B roster/lobby = %+v / %+v", b.Players, b.Lobby)
	}

	// Room A's players cannot write into room B.
	if err := f.store.SaveDraft(ctx, roomB, alice.UserID, domain.Draft{Content: "x", UpdatedAt: baseTime}); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("SaveDraft(room B, alice) error = %v, want ErrNotGamePlayer", err)
	}
	if _, err := f.store.IncrementPoints(ctx, roomB, alice.UserID, 1); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("IncrementPoints(room B, alice) error = %v, want ErrNotGamePlayer", err)
	}

	// Deleting room B leaves room A intact.
	if err := f.store.Delete(ctx, roomB); err != nil {
		t.Fatal(err)
	}
	a, err := f.store.Load(ctx, roomA, alice.UserID)
	if err != nil || a.Draft == nil || a.Points[alice.UserID] != 50 {
		t.Errorf("room A after deleting room B = %+v, %v", a, err)
	}
}

func TestStoreEveryKeyHasTTL(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)

	writes := []struct {
		name string
		run  func() error
	}{
		{"SetLobby", func() error { return f.store.SetLobby(ctx, lobbyFor(gameID, a.UserID, domain.GameStatusWaiting)) }},
		{"AddPlayer", func() error { return f.store.AddPlayer(ctx, gameID, a) }},
		{"AddPlayer b", func() error { return f.store.AddPlayer(ctx, gameID, b) }},
		{"SetReady", func() error { return f.store.SetReady(ctx, gameID, a.UserID) }},
		{"SetRound", func() error {
			return f.store.SetRound(ctx, gameID, domain.RoundState{Number: 1, ChallengeID: uuid.New(), StartedAt: baseTime, Deadline: baseTime.Add(time.Minute)})
		}},
		{"SaveDraft a", func() error {
			return f.store.SaveDraft(ctx, gameID, a.UserID, domain.Draft{Content: "x", UpdatedAt: baseTime})
		}},
		{"SaveDraft b", func() error {
			return f.store.SaveDraft(ctx, gameID, b.UserID, domain.Draft{Content: "y", UpdatedAt: baseTime})
		}},
		{"IncrementPoints", func() error { _, err := f.store.IncrementPoints(ctx, gameID, b.UserID, 1); return err }},
		{"UnsetReady", func() error { return f.store.UnsetReady(ctx, gameID, a.UserID) }},
		{"SyncLobby", func() error {
			return f.store.SyncLobby(ctx, lobbyFor(gameID, a.UserID, domain.GameStatusInProgress), []domain.LivePlayer{a, b})
		}},
	}
	for _, w := range writes {
		if err := w.run(); err != nil {
			t.Fatalf("%s error = %v", w.name, err)
		}
		f.assertAllKeysExpire(t, testActiveTTL)
	}

	// 6 keys: state, players, ready is empty after UnsetReady (and so removed),
	// round, points, and two drafts.
	if keys := redistest.Keys(t, f.client, f.prefix); len(keys) != 6 {
		t.Errorf("keys = %v, want state, players, round, points and 2 drafts", keys)
	}
}

func TestStoreTTLSlidesOnWritesAndShortensWhenGameEnds(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a := player(uuid.New(), "a", 0)
	f.seedGame(t, gameID, a)
	if err := f.store.SaveDraft(ctx, gameID, a.UserID, domain.Draft{Content: "x", UpdatedAt: baseTime}); err != nil {
		t.Fatal(err)
	}

	// Simulate time passing: expire everything soon, then write again.
	for _, k := range redistest.Keys(t, f.client, f.prefix) {
		f.client.PExpire(ctx, k, 5*time.Second)
	}
	if err := f.store.SetReady(ctx, gameID, a.UserID); err != nil {
		t.Fatal(err)
	}
	for _, k := range redistest.Keys(t, f.client, f.prefix) {
		if ttl := f.client.PTTL(ctx, k).Val(); ttl < testActiveTTL-time.Minute {
			t.Errorf("key %s TTL = %s after a write, want it refreshed to about %s", k, ttl, testActiveTTL)
		}
	}

	ended := lobbyFor(gameID, a.UserID, domain.GameStatusCancelled)
	if err := f.store.SetLobby(ctx, ended); err != nil {
		t.Fatal(err)
	}
	f.assertAllKeysExpire(t, testEndedTTL)
}

func TestStoreDeleteRemovesEveryKey(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a, b := player(uuid.New(), "a", 0), player(uuid.New(), "b", time.Second)
	f.seedGame(t, gameID, a, b)
	_ = f.store.SetReady(ctx, gameID, a.UserID)
	_ = f.store.SaveDraft(ctx, gameID, a.UserID, domain.Draft{Content: "x", UpdatedAt: baseTime})
	_ = f.store.SaveDraft(ctx, gameID, b.UserID, domain.Draft{Content: "y", UpdatedAt: baseTime})
	_, _ = f.store.IncrementPoints(ctx, gameID, b.UserID, 1)

	if err := f.store.Delete(ctx, gameID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if keys := redistest.Keys(t, f.client, f.prefix); len(keys) != 0 {
		t.Errorf("keys left after Delete() = %v", keys)
	}
}

func TestStoreCorruptDataIsReportedNotPanicked(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	gameID := uuid.New()
	a := player(uuid.New(), "a", 0)
	f.seedGame(t, gameID, a)
	gk, _ := f.store.keys.Game(gameID)
	draftKey, _ := gk.Draft(a.UserID)

	f.client.HSet(ctx, gk.Players, a.UserID.String(), "{not json")
	f.client.Set(ctx, draftKey, "[]", time.Minute)
	f.client.HSet(ctx, gk.State, "settings", "nope")

	checks := map[string]error{}
	_, checks["ListPlayers"] = f.store.ListPlayers(ctx, gameID)
	_, checks["GetDraft"] = f.store.GetDraft(ctx, gameID, a.UserID)
	_, checks["GetLobby"] = f.store.GetLobby(ctx, gameID)
	_, checks["Load"] = f.store.Load(ctx, gameID, a.UserID)
	for name, err := range checks {
		if err == nil || !strings.Contains(err.Error(), "corrupt") {
			t.Errorf("%s error = %v, want a corrupt data error", name, err)
		}
		if errors.Is(err, domain.ErrStateNotFound) || errors.Is(err, domain.ErrStateUnavailable) {
			t.Errorf("%s error = %v, must not look like missing or unavailable state", name, err)
		}
	}

	// A key of the wrong type is a usage bug, not an outage.
	f.client.Del(ctx, gk.Ready)
	f.client.Set(ctx, gk.Ready, "string", time.Minute)
	err := f.store.SetReady(ctx, gameID, a.UserID)
	if err == nil || errors.Is(err, domain.ErrStateUnavailable) {
		t.Errorf("SetReady() on a WRONGTYPE key error = %v, want an internal error", err)
	}
}

func TestStoreRejectsNilIDs(t *testing.T) {
	f := newStoreFixture(t)
	ctx := context.Background()
	id := uuid.New()

	checks := map[string]error{
		"GetLobby(nil game)":     func() error { _, err := f.store.GetLobby(ctx, uuid.Nil); return err }(),
		"SaveDraft(nil user)":    f.store.SaveDraft(ctx, id, uuid.Nil, domain.Draft{}),
		"GetDraft(nil user)":     func() error { _, err := f.store.GetDraft(ctx, id, uuid.Nil); return err }(),
		"Load(nil viewer)":       func() error { _, err := f.store.Load(ctx, id, uuid.Nil); return err }(),
		"SetReady(nil game)":     f.store.SetReady(ctx, uuid.Nil, id),
		"IncrementPoints(nil)":   func() error { _, err := f.store.IncrementPoints(ctx, id, uuid.Nil, 1); return err }(),
		"AddPlayer(nil user)":    f.store.AddPlayer(ctx, id, domain.LivePlayer{}),
		"RemovePlayer(nil user)": f.store.RemovePlayer(ctx, id, uuid.Nil),
		"Delete(nil game)":       f.store.Delete(ctx, uuid.Nil),
		"ListReady(nil game)":    func() error { _, err := f.store.ListReady(ctx, uuid.Nil); return err }(),
		"GetPoints(nil game)":    func() error { _, err := f.store.GetPoints(ctx, uuid.Nil); return err }(),
		"GetRound(nil game)":     func() error { _, err := f.store.GetRound(ctx, uuid.Nil); return err }(),
		"ListPlayers(nil game)":  func() error { _, err := f.store.ListPlayers(ctx, uuid.Nil); return err }(),
		"SetRound(nil game)":     f.store.SetRound(ctx, uuid.Nil, domain.RoundState{Number: 1, ChallengeID: id, StartedAt: baseTime, Deadline: baseTime.Add(time.Second)}),
		"SyncLobby(nil game)":    f.store.SyncLobby(ctx, lobbyFor(uuid.Nil, id, domain.GameStatusWaiting), nil),
		"UnsetReady(nil user)":   f.store.UnsetReady(ctx, id, uuid.Nil),
	}
	for name, err := range checks {
		if !errors.Is(err, ErrInvalidKeyID) {
			t.Errorf("%s error = %v, want ErrInvalidKeyID", name, err)
		}
	}
	if keys := redistest.Keys(t, f.client, f.prefix); len(keys) != 0 {
		t.Errorf("keys written despite invalid IDs: %v", keys)
	}
}

func TestStoreUnavailableRedisReturnsControlledErrors(t *testing.T) {
	gameID, userID := uuid.New(), uuid.New()
	opts := Options{KeyPrefix: "promptgame", ActiveTTL: time.Hour, EndedTTL: time.Minute, OpTimeout: 300 * time.Millisecond}

	closed := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	closed.Close()
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 100 * time.Millisecond, MaxRetries: -1, ContextTimeoutEnabled: true})
	defer unreachable.Close()

	for name, client := range map[string]*redis.Client{"closed client": closed, "unreachable server": unreachable} {
		t.Run(name, func(t *testing.T) {
			store, err := New(client, opts)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			lobby := lobbyFor(gameID, userID, domain.GameStatusWaiting)

			calls := map[string]func() error{
				"SetLobby":        func() error { return store.SetLobby(ctx, lobby) },
				"SyncLobby":       func() error { return store.SyncLobby(ctx, lobby, nil) },
				"GetLobby":        func() error { _, err := store.GetLobby(ctx, gameID); return err },
				"AddPlayer":       func() error { return store.AddPlayer(ctx, gameID, player(userID, "a", 0)) },
				"ListPlayers":     func() error { _, err := store.ListPlayers(ctx, gameID); return err },
				"SetReady":        func() error { return store.SetReady(ctx, gameID, userID) },
				"ListReady":       func() error { _, err := store.ListReady(ctx, gameID); return err },
				"GetRound":        func() error { _, err := store.GetRound(ctx, gameID); return err },
				"SaveDraft":       func() error { return store.SaveDraft(ctx, gameID, userID, domain.Draft{Content: "x"}) },
				"GetDraft":        func() error { _, err := store.GetDraft(ctx, gameID, userID); return err },
				"IncrementPoints": func() error { _, err := store.IncrementPoints(ctx, gameID, userID, 1); return err },
				"GetPoints":       func() error { _, err := store.GetPoints(ctx, gameID); return err },
				"Load":            func() error { _, err := store.Load(ctx, gameID, userID); return err },
				"Delete":          func() error { return store.Delete(ctx, gameID) },
			}
			for op, call := range calls {
				start := time.Now()
				err := call()
				if !errors.Is(err, domain.ErrStateUnavailable) {
					t.Errorf("%s error = %v, want ErrStateUnavailable", op, err)
				}
				if elapsed := time.Since(start); elapsed > 2*time.Second {
					t.Errorf("%s took %s, want it bounded by the operation timeout", op, elapsed)
				}
			}
		})
	}
}
