package service

import (
	"context"
	"errors"
	"log"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// fakeGameState is an in-memory repository.GameStateRepository that follows
// the Redis store's rules: membership checks on ready, draft and points
// writes, and drafts keyed by user.
type fakeGameState struct {
	mu    sync.Mutex
	games map[uuid.UUID]*fakeLiveGame
	// err, when set, is returned by every call.
	err error
	// syncs counts SyncLobby calls; syncCtxErr is the context error seen by
	// the last one.
	syncs      int
	syncCtxErr error
}

type fakeLiveGame struct {
	lobby   *domain.LobbyState
	players map[uuid.UUID]domain.LivePlayer
	ready   map[uuid.UUID]bool
	round   *domain.RoundState
	drafts  map[uuid.UUID]domain.Draft
	points  map[uuid.UUID]int64
}

func newFakeGameState() *fakeGameState {
	return &fakeGameState{games: map[uuid.UUID]*fakeLiveGame{}}
}

func (f *fakeGameState) game(id uuid.UUID) *fakeLiveGame {
	g, ok := f.games[id]
	if !ok {
		g = &fakeLiveGame{
			players: map[uuid.UUID]domain.LivePlayer{},
			ready:   map[uuid.UUID]bool{},
			drafts:  map[uuid.UUID]domain.Draft{},
			points:  map[uuid.UUID]int64{},
		}
		f.games[id] = g
	}
	return g
}

func (f *fakeGameState) lock() func() {
	f.mu.Lock()
	return f.mu.Unlock
}

func (f *fakeGameState) SetLobby(_ context.Context, l domain.LobbyState) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	f.game(l.GameID).lobby = &l
	return nil
}

func (f *fakeGameState) GetLobby(_ context.Context, id uuid.UUID) (*domain.LobbyState, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	g, ok := f.games[id]
	if !ok || g.lobby == nil {
		return nil, domain.ErrStateNotFound
	}
	l := *g.lobby
	return &l, nil
}

func (f *fakeGameState) SyncLobby(ctx context.Context, l domain.LobbyState, players []domain.LivePlayer) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	f.syncs++
	f.syncCtxErr = ctx.Err()
	g := f.game(l.GameID)
	g.lobby = &l
	keep := map[uuid.UUID]bool{}
	for _, p := range players {
		keep[p.UserID] = true
		g.players[p.UserID] = p
	}
	for id := range g.players {
		if !keep[id] {
			delete(g.players, id)
			delete(g.ready, id)
			delete(g.drafts, id)
		}
	}
	return nil
}

func (f *fakeGameState) AddPlayer(_ context.Context, id uuid.UUID, p domain.LivePlayer) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	f.game(id).players[p.UserID] = p
	return nil
}

func (f *fakeGameState) RemovePlayer(_ context.Context, id, userID uuid.UUID) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	g := f.game(id)
	delete(g.players, userID)
	delete(g.ready, userID)
	delete(g.drafts, userID)
	return nil
}

func (f *fakeGameState) ListPlayers(_ context.Context, id uuid.UUID) ([]domain.LivePlayer, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	return f.sortedPlayers(f.game(id)), nil
}

func (f *fakeGameState) sortedPlayers(g *fakeLiveGame) []domain.LivePlayer {
	players := slices.Collect(maps.Values(g.players))
	slices.SortFunc(players, func(a, b domain.LivePlayer) int { return a.JoinedAt.Compare(b.JoinedAt) })
	return players
}

func (f *fakeGameState) setReady(id, userID uuid.UUID, ready bool) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	g := f.game(id)
	if _, ok := g.players[userID]; !ok {
		return domain.ErrNotGamePlayer
	}
	if ready {
		g.ready[userID] = true
	} else {
		delete(g.ready, userID)
	}
	return nil
}

func (f *fakeGameState) SetReady(_ context.Context, id, userID uuid.UUID) error {
	return f.setReady(id, userID, true)
}

func (f *fakeGameState) UnsetReady(_ context.Context, id, userID uuid.UUID) error {
	return f.setReady(id, userID, false)
}

func (f *fakeGameState) ListReady(_ context.Context, id uuid.UUID) ([]uuid.UUID, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	return slices.Collect(maps.Keys(f.game(id).ready)), nil
}

func (f *fakeGameState) SetRound(_ context.Context, id uuid.UUID, r domain.RoundState) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	f.game(id).round = &r
	return nil
}

func (f *fakeGameState) GetRound(_ context.Context, id uuid.UUID) (*domain.RoundState, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	g := f.game(id)
	if g.round == nil {
		return nil, domain.ErrStateNotFound
	}
	r := *g.round
	return &r, nil
}

func (f *fakeGameState) SaveDraft(_ context.Context, id, userID uuid.UUID, d domain.Draft) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	g := f.game(id)
	if _, ok := g.players[userID]; !ok {
		return domain.ErrNotGamePlayer
	}
	g.drafts[userID] = d
	return nil
}

func (f *fakeGameState) GetDraft(_ context.Context, id, userID uuid.UUID) (*domain.Draft, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	d, ok := f.game(id).drafts[userID]
	if !ok {
		return nil, domain.ErrStateNotFound
	}
	return &d, nil
}

func (f *fakeGameState) IncrementPoints(_ context.Context, id, userID uuid.UUID, delta int64) (int64, error) {
	defer f.lock()()
	if f.err != nil {
		return 0, f.err
	}
	g := f.game(id)
	if _, ok := g.players[userID]; !ok {
		return 0, domain.ErrNotGamePlayer
	}
	g.points[userID] += delta
	return g.points[userID], nil
}

func (f *fakeGameState) GetPoints(_ context.Context, id uuid.UUID) (map[uuid.UUID]int64, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	return maps.Clone(f.game(id).points), nil
}

func (f *fakeGameState) Load(_ context.Context, id, viewerID uuid.UUID) (*domain.LiveGameState, error) {
	defer f.lock()()
	if f.err != nil {
		return nil, f.err
	}
	g, ok := f.games[id]
	if !ok || g.lobby == nil {
		return nil, domain.ErrStateNotFound
	}
	live := &domain.LiveGameState{
		Lobby:   *g.lobby,
		Players: f.sortedPlayers(g),
		Ready:   slices.Collect(maps.Keys(g.ready)),
		Points:  maps.Clone(g.points),
	}
	if g.round != nil {
		r := *g.round
		live.Round = &r
	}
	if d, ok := g.drafts[viewerID]; ok {
		live.Draft = &d
	}
	return live, nil
}

func (f *fakeGameState) Delete(_ context.Context, id uuid.UUID) error {
	defer f.lock()()
	if f.err != nil {
		return f.err
	}
	delete(f.games, id)
	return nil
}

var liveNow = time.Date(2026, 9, 18, 15, 0, 0, 0, time.UTC)

type liveFixture struct {
	games  *fakeGameStore
	state  *fakeGameState
	logs   *strings.Builder
	svc    *LiveStateService
	host   uuid.UUID
	guest  uuid.UUID
	game   *domain.Game
	gameID string
}

// newLiveFixture seeds a PostgreSQL game (host and guest) in the given status
// and, when inRedis, mirrors it into live state.
func newLiveFixture(t *testing.T, status domain.GameStatus, inRedis bool) *liveFixture {
	t.Helper()
	f := &liveFixture{games: newFakeGameStore(), state: newFakeGameState(), logs: &strings.Builder{}, host: uuid.New(), guest: uuid.New()}
	f.games.usernames[f.host], f.games.usernames[f.guest] = "host", "guest"
	f.svc = NewLiveStateService(f.state, f.games, log.New(f.logs, "", 0), func() time.Time { return liveNow })
	f.game = f.games.seed(status, "778899", validSettings, f.host, f.guest)
	f.gameID = f.game.ID.String()
	if inRedis {
		f.svc.SyncGame(context.Background(), f.game)
		f.state.syncs = 0
	}
	return f
}

func TestLiveStateGetLiveStateFromRedis(t *testing.T) {
	f := newLiveFixture(t, domain.GameStatusInProgress, true)
	ctx := context.Background()
	challenge := uuid.New()
	round := domain.RoundState{Number: 2, ChallengeID: challenge, StartedAt: liveNow.Add(-30 * time.Second), Deadline: liveNow.Add(60 * time.Second)}
	_ = f.state.SetRound(ctx, f.game.ID, round)
	_ = f.state.SaveDraft(ctx, f.game.ID, f.host, domain.Draft{Content: "host's secret"})
	_ = f.state.SaveDraft(ctx, f.game.ID, f.guest, domain.Draft{Content: "guest's secret"})
	_, _ = f.state.IncrementPoints(ctx, f.game.ID, f.guest, 9)

	for _, ref := range []string{f.gameID, f.game.RoomCode} {
		got, err := f.svc.GetLiveState(ctx, f.guest, ref)
		if err != nil {
			t.Fatalf("GetLiveState(%s) error = %v", ref, err)
		}
		if got.Rebuilt {
			t.Error("Rebuilt = true, want state served from Redis")
		}
		if got.Lobby.Status != domain.GameStatusInProgress || len(got.Players) != 2 {
			t.Errorf("lobby/players = %+v / %+v", got.Lobby, got.Players)
		}
		if got.Round == nil || got.Round.ChallengeID != challenge {
			t.Errorf("Round = %+v", got.Round)
		}
		if got.RoundRemaining != 60*time.Second {
			t.Errorf("RoundRemaining = %s, want 60s computed from the server deadline", got.RoundRemaining)
		}
		if got.Draft == nil || got.Draft.Content != "guest's secret" {
			t.Errorf("Draft = %+v, want only the caller's own draft", got.Draft)
		}
		if got.Points[f.guest] != 9 {
			t.Errorf("Points = %v", got.Points)
		}
	}

	// After the deadline the remaining time is zero, never negative.
	_ = f.state.SetRound(ctx, f.game.ID, domain.RoundState{Number: 2, ChallengeID: challenge, StartedAt: liveNow.Add(-2 * time.Minute), Deadline: liveNow.Add(-time.Minute)})
	got, _ := f.svc.GetLiveState(ctx, f.host, f.gameID)
	if got.RoundRemaining != 0 {
		t.Errorf("RoundRemaining after the deadline = %s, want 0", got.RoundRemaining)
	}
}

func TestLiveStateGetLiveStateAccessControl(t *testing.T) {
	ctx := context.Background()

	t.Run("outsider is refused", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusWaiting, true)
		if _, err := f.svc.GetLiveState(ctx, uuid.New(), f.gameID); !errors.Is(err, domain.ErrNotGamePlayer) {
			t.Errorf("error = %v, want ErrNotGamePlayer", err)
		}
	})

	t.Run("outsider is refused when state is rebuilt", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusWaiting, false)
		if _, err := f.svc.GetLiveState(ctx, uuid.New(), f.gameID); !errors.Is(err, domain.ErrNotGamePlayer) {
			t.Errorf("error = %v, want ErrNotGamePlayer", err)
		}
	})

	t.Run("player missing from a stale roster is healed", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusWaiting, true)
		_ = f.state.RemovePlayer(ctx, f.game.ID, f.guest)

		got, err := f.svc.GetLiveState(ctx, f.guest, f.gameID)
		if err != nil {
			t.Fatalf("error = %v, want the roster healed from PostgreSQL", err)
		}
		if !hasLivePlayer(got.Players, f.guest) || f.state.syncs != 1 {
			t.Errorf("players = %+v, syncs = %d; want guest restored by one sync", got.Players, f.state.syncs)
		}
	})

	t.Run("unknown or malformed references", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusWaiting, true)
		for _, ref := range []string{uuid.NewString(), "000000", "nope", uuid.Nil.String()} {
			if _, err := f.svc.GetLiveState(ctx, f.host, ref); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("GetLiveState(%q) error = %v, want ErrNotFound", ref, err)
			}
		}
	})
}

func TestLiveStateGetLiveStateRebuildsFromPostgres(t *testing.T) {
	ctx := context.Background()

	for _, tt := range []struct {
		status     domain.GameStatus
		wantSynced bool
	}{
		{domain.GameStatusWaiting, true},
		{domain.GameStatusInProgress, true},
		{domain.GameStatusCancelled, false},
	} {
		t.Run(string(tt.status), func(t *testing.T) {
			f := newLiveFixture(t, tt.status, false)

			got, err := f.svc.GetLiveState(ctx, f.host, f.gameID)
			if err != nil {
				t.Fatalf("error = %v", err)
			}
			if !got.Rebuilt {
				t.Error("Rebuilt = false, want true")
			}
			if got.Lobby.HostID != f.host || got.Lobby.Status != tt.status || got.Lobby.Settings != validSettings {
				t.Errorf("Lobby = %+v", got.Lobby)
			}
			if len(got.Players) != 2 || got.Players[0].UserID != f.host || got.Players[1].Username != "guest" {
				t.Errorf("Players = %+v", got.Players)
			}
			if got.Round != nil || got.Draft != nil || len(got.Ready) != 0 || len(got.Points) != 0 {
				t.Errorf("rebuilt state = %+v, want no Redis-only data", got)
			}
			if synced := f.state.syncs == 1; synced != tt.wantSynced {
				t.Errorf("written back to Redis = %v, want %v", synced, tt.wantSynced)
			}
		})
	}
}

func TestLiveStateUnavailableRedisIsReportedAndLogged(t *testing.T) {
	ctx := context.Background()
	f := newLiveFixture(t, domain.GameStatusInProgress, true)
	f.state.err = errors.Join(domain.ErrStateUnavailable, errors.New("dial tcp: connection refused"))

	calls := map[string]error{}
	_, calls["GetLiveState"] = f.svc.GetLiveState(ctx, f.host, f.gameID)
	calls["SetReady"] = f.svc.SetReady(ctx, f.host, f.gameID, true)
	_, calls["SaveDraft"] = f.svc.SaveDraft(ctx, f.host, f.gameID, "secret prompt text")
	_, calls["GetDraft"] = f.svc.GetDraft(ctx, f.host, f.gameID)
	_, calls["AwardPoints"] = f.svc.AwardPoints(ctx, f.game.ID, f.host, 1)
	_, calls["StartRound"] = f.svc.StartRound(ctx, f.game.ID, 1, uuid.New())
	calls["ClearGame"] = f.svc.ClearGame(ctx, f.game.ID)

	for name, err := range calls {
		if !errors.Is(err, domain.ErrStateUnavailable) {
			t.Errorf("%s error = %v, want ErrStateUnavailable", name, err)
		}
	}
	logs := f.logs.String()
	if !strings.Contains(logs, "unavailable=true") {
		t.Errorf("logs = %q, want structured unavailable entries", logs)
	}
	if strings.Contains(logs, "secret prompt text") {
		t.Error("draft content was written to the log")
	}

	// Sync failures are logged and swallowed.
	f.svc.SyncGame(ctx, f.game)
	if !strings.Contains(f.logs.String(), "op=sync") {
		t.Error("sync failure was not logged")
	}
}

func TestLiveStateSetReady(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name    string
		status  domain.GameStatus
		inRedis bool
		caller  func(f *liveFixture) uuid.UUID
		wantErr error
	}{
		{name: "player in a waiting game", status: domain.GameStatusWaiting, inRedis: true, caller: func(f *liveFixture) uuid.UUID { return f.guest }},
		{name: "state rebuilt when Redis is empty", status: domain.GameStatusWaiting, inRedis: false, caller: func(f *liveFixture) uuid.UUID { return f.guest }},
		{name: "outsider", status: domain.GameStatusWaiting, inRedis: true, caller: func(*liveFixture) uuid.UUID { return uuid.New() }, wantErr: domain.ErrNotGamePlayer},
		{name: "game already started", status: domain.GameStatusInProgress, inRedis: true, caller: func(f *liveFixture) uuid.UUID { return f.guest }, wantErr: domain.ErrGameNotWaiting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newLiveFixture(t, tt.status, tt.inRedis)
			caller := tt.caller(f)

			err := f.svc.SetReady(ctx, caller, f.gameID, true)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("SetReady() error = %v, want %v", err, tt.wantErr)
			}
			ready, _ := f.state.ListReady(ctx, f.game.ID)
			if tt.wantErr != nil {
				if len(ready) != 0 {
					t.Errorf("ready = %v after a refused request", ready)
				}
				return
			}
			if !slices.Equal(ready, []uuid.UUID{caller}) {
				t.Errorf("ready = %v, want [%s]", ready, caller)
			}
			if err := f.svc.SetReady(ctx, caller, f.gameID, false); err != nil {
				t.Fatalf("SetReady(false) error = %v", err)
			}
			if ready, _ := f.state.ListReady(ctx, f.game.ID); len(ready) != 0 {
				t.Errorf("ready = %v after unsetting", ready)
			}
		})
	}

	t.Run("stale roster is healed", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusWaiting, true)
		_ = f.state.RemovePlayer(ctx, f.game.ID, f.guest)
		if err := f.svc.SetReady(ctx, f.guest, f.gameID, true); err != nil {
			t.Fatalf("SetReady() error = %v, want success after healing", err)
		}
	})
}

func TestLiveStateDrafts(t *testing.T) {
	ctx := context.Background()

	t.Run("save and read own draft with the current round", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusInProgress, true)
		_ = f.state.SetRound(ctx, f.game.ID, domain.RoundState{Number: 3, ChallengeID: uuid.New(), StartedAt: liveNow, Deadline: liveNow.Add(time.Minute)})

		saved, err := f.svc.SaveDraft(ctx, f.host, f.gameID, "my prompt")
		if err != nil {
			t.Fatalf("SaveDraft() error = %v", err)
		}
		if saved.RoundNumber != 3 || !saved.UpdatedAt.Equal(liveNow) {
			t.Errorf("saved = %+v, want round 3 stamped with server time", saved)
		}
		got, err := f.svc.GetDraft(ctx, f.host, f.gameID)
		if err != nil || got.Content != "my prompt" {
			t.Errorf("GetDraft(host) = %+v, %v", got, err)
		}
	})

	t.Run("another player cannot read it", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusInProgress, true)
		if _, err := f.svc.SaveDraft(ctx, f.host, f.gameID, "host only"); err != nil {
			t.Fatal(err)
		}
		for name, caller := range map[string]uuid.UUID{"other player": f.guest, "outsider": uuid.New()} {
			got, err := f.svc.GetDraft(ctx, caller, f.gameID)
			if !errors.Is(err, domain.ErrStateNotFound) || got != nil {
				t.Errorf("GetDraft(%s) = %+v, %v; want ErrStateNotFound", name, got, err)
			}
		}
		live, err := f.svc.GetLiveState(ctx, f.guest, f.gameID)
		if err != nil || live.Draft != nil {
			t.Errorf("guest's live state draft = %+v, %v; want none", live.Draft, err)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		tests := []struct {
			name       string
			status     domain.GameStatus
			caller     func(f *liveFixture) uuid.UUID
			content    string
			wantErr    error
			wantFields bool
		}{
			{name: "blank", status: domain.GameStatusInProgress, caller: func(f *liveFixture) uuid.UUID { return f.host }, content: "  ", wantFields: true},
			{name: "too long", status: domain.GameStatusInProgress, caller: func(f *liveFixture) uuid.UUID { return f.host }, content: strings.Repeat("a", domain.MaxDraftBytes+1), wantFields: true},
			{name: "invalid UTF-8", status: domain.GameStatusInProgress, caller: func(f *liveFixture) uuid.UUID { return f.host }, content: "bad \xff", wantFields: true},
			{name: "game not in progress", status: domain.GameStatusWaiting, caller: func(f *liveFixture) uuid.UUID { return f.host }, content: "x", wantErr: domain.ErrGameNotInProgress},
			{name: "outsider", status: domain.GameStatusInProgress, caller: func(*liveFixture) uuid.UUID { return uuid.New() }, content: "x", wantErr: domain.ErrNotGamePlayer},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				f := newLiveFixture(t, tt.status, true)
				_, err := f.svc.SaveDraft(ctx, tt.caller(f), f.gameID, tt.content)
				if tt.wantFields {
					assertValidationFields(t, err, "content")
					return
				}
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("SaveDraft() error = %v, want %v", err, tt.wantErr)
				}
			})
		}
	})
}

func TestLiveStateStartRound(t *testing.T) {
	ctx := context.Background()
	challenge := uuid.New()

	t.Run("deadline comes from the server clock and settings", func(t *testing.T) {
		f := newLiveFixture(t, domain.GameStatusInProgress, true)
		round, err := f.svc.StartRound(ctx, f.game.ID, 1, challenge)
		if err != nil {
			t.Fatalf("StartRound() error = %v", err)
		}
		wantDeadline := liveNow.Add(time.Duration(validSettings.TimePerRoundSeconds) * time.Second)
		if !round.StartedAt.Equal(liveNow) || !round.Deadline.Equal(wantDeadline) {
			t.Errorf("round = %+v, want start %s and deadline %s", round, liveNow, wantDeadline)
		}
		if stored, _ := f.state.GetRound(ctx, f.game.ID); stored == nil || *stored != *round {
			t.Errorf("stored round = %+v, want %+v", stored, round)
		}
	})

	t.Run("refusals", func(t *testing.T) {
		waiting := newLiveFixture(t, domain.GameStatusWaiting, true)
		if _, err := waiting.svc.StartRound(ctx, waiting.game.ID, 1, challenge); !errors.Is(err, domain.ErrGameNotInProgress) {
			t.Errorf("StartRound() on a waiting game error = %v, want ErrGameNotInProgress", err)
		}
		f := newLiveFixture(t, domain.GameStatusInProgress, true)
		for _, n := range []int{0, validSettings.Rounds + 1} {
			if _, err := f.svc.StartRound(ctx, f.game.ID, n, challenge); err == nil {
				t.Errorf("StartRound(%d) error = nil, want out of range", n)
			}
		}
	})
}

func TestLiveStateAwardPointsAndClear(t *testing.T) {
	ctx := context.Background()
	f := newLiveFixture(t, domain.GameStatusInProgress, true)

	if total, err := f.svc.AwardPoints(ctx, f.game.ID, f.guest, 4); err != nil || total != 4 {
		t.Fatalf("AwardPoints() = %d, %v; want 4", total, err)
	}
	if _, err := f.svc.AwardPoints(ctx, f.game.ID, uuid.New(), 4); !errors.Is(err, domain.ErrNotGamePlayer) {
		t.Errorf("AwardPoints(outsider) error = %v, want ErrNotGamePlayer", err)
	}

	if err := f.svc.ClearGame(ctx, f.game.ID); err != nil {
		t.Fatalf("ClearGame() error = %v", err)
	}
	if _, err := f.state.Load(ctx, f.game.ID, f.host); !errors.Is(err, domain.ErrStateNotFound) {
		t.Errorf("state after ClearGame() = %v, want ErrStateNotFound", err)
	}
}

func TestLiveStateSyncGameSurvivesCancelledContext(t *testing.T) {
	f := newLiveFixture(t, domain.GameStatusWaiting, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// The lobby change is already committed, so a client hanging up must not
	// cancel the mirror write.
	f.svc.SyncGame(ctx, f.game)
	if f.state.syncs != 1 || f.state.syncCtxErr != nil {
		t.Errorf("syncs = %d, context error = %v; want one sync with a live context", f.state.syncs, f.state.syncCtxErr)
	}
}
