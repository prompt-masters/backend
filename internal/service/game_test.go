package service

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// fakeGameStore is an in-memory repository.Transactor. Transactions are
// serialized by one mutex, standing in for the game row lock, and a failed
// transaction restores the state it started from.
type fakeGameStore struct {
	mu         sync.Mutex
	games      map[uuid.UUID]*domain.Game
	challenges *fakeChallengeRepository
	usernames  map[uuid.UUID]string
	clock      time.Time
	// addPlayerErr, when set, makes AddPlayer fail.
	addPlayerErr error
}

func newFakeGameStore() *fakeGameStore {
	return &fakeGameStore{
		games:     map[uuid.UUID]*domain.Game{},
		usernames: map[uuid.UUID]string{},
		clock:     time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC),
		challenges: &fakeChallengeRepository{challenges: []*domain.Challenge{
			newChallenge("coding-medium", domain.CategoryCoding, domain.DifficultyMedium),
		}},
	}
}

func (s *fakeGameStore) WithinTx(ctx context.Context, fn func(repository.TxRepositories) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot := make(map[uuid.UUID]*domain.Game, len(s.games))
	for id, g := range s.games {
		snapshot[id] = cloneGame(g)
	}
	if err := fn(repository.TxRepositories{Games: fakeGameRepo{s}, Challenges: s.challenges}); err != nil {
		s.games = snapshot
		return err
	}
	return nil
}

func cloneGame(g *domain.Game) *domain.Game {
	c := *g
	c.Players = slices.Clone(g.Players)
	c.PlayerCount = len(c.Players)
	return &c
}

func (s *fakeGameStore) tick() time.Time {
	s.clock = s.clock.Add(time.Second)
	return s.clock
}

// seed stores a game directly, bypassing the service.
func (s *fakeGameStore) seed(status domain.GameStatus, code string, settings domain.GameSettings, players ...uuid.UUID) *domain.Game {
	g := &domain.Game{ID: uuid.New(), RoomCode: code, HostID: players[0], Status: status, Settings: settings}
	for _, p := range players {
		g.Players = append(g.Players, domain.GamePlayer{UserID: p, Username: s.usernames[p], JoinedAt: s.tick()})
	}
	g.PlayerCount = len(g.Players)
	s.games[g.ID] = g
	return cloneGame(g)
}

func (s *fakeGameStore) get(id uuid.UUID) *domain.Game {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneGame(s.games[id])
}

type fakeGameRepo struct{ s *fakeGameStore }

func (r fakeGameRepo) Create(_ context.Context, hostID uuid.UUID, code string) (*domain.Game, error) {
	for _, g := range r.s.games {
		if g.RoomCode == code && g.Status.Active() {
			return nil, domain.ErrRoomCodeTaken
		}
	}
	g := &domain.Game{ID: uuid.New(), RoomCode: code, HostID: hostID, Status: domain.GameStatusWaiting}
	r.s.games[g.ID] = g
	return cloneGame(g), nil
}

func (r fakeGameRepo) CreateSettings(_ context.Context, id uuid.UUID, settings domain.GameSettings) error {
	r.s.games[id].Settings = settings
	return nil
}

func (r fakeGameRepo) UpdateSettings(_ context.Context, id uuid.UUID, settings domain.GameSettings) error {
	r.s.games[id].Settings = settings
	return nil
}

func (r fakeGameRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Game, error) {
	g, ok := r.s.games[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	return cloneGame(g), nil
}

func (r fakeGameRepo) LockByID(ctx context.Context, id uuid.UUID) (*domain.Game, error) {
	return r.GetByID(ctx, id)
}

func (r fakeGameRepo) GetActiveIDByRoomCode(_ context.Context, code string) (uuid.UUID, error) {
	for id, g := range r.s.games {
		if g.RoomCode == code && g.Status.Active() {
			return id, nil
		}
	}
	return uuid.Nil, domain.ErrNotFound
}

func (r fakeGameRepo) ListByStatus(_ context.Context, status domain.GameStatus, limit int) ([]*domain.Game, error) {
	out := []*domain.Game{}
	for _, g := range r.s.games {
		if g.Status == status && len(out) < limit {
			c := cloneGame(g)
			c.Players = nil
			out = append(out, c)
		}
	}
	return out, nil
}

func (r fakeGameRepo) AddPlayer(_ context.Context, gameID, userID uuid.UUID) error {
	if r.s.addPlayerErr != nil {
		return r.s.addPlayerErr
	}
	g := r.s.games[gameID]
	if g.HasPlayer(userID) {
		return domain.ErrAlreadyJoined
	}
	g.Players = append(g.Players, domain.GamePlayer{UserID: userID, Username: r.s.usernames[userID], JoinedAt: r.s.tick()})
	g.PlayerCount = len(g.Players)
	return nil
}

func (r fakeGameRepo) RemovePlayer(_ context.Context, gameID, userID uuid.UUID) error {
	g := r.s.games[gameID]
	i := slices.IndexFunc(g.Players, func(p domain.GamePlayer) bool { return p.UserID == userID })
	if i < 0 {
		return domain.ErrNotInGame
	}
	g.Players = slices.Delete(g.Players, i, i+1)
	g.PlayerCount = len(g.Players)
	return nil
}

func (r fakeGameRepo) UpdateHost(_ context.Context, gameID, hostID uuid.UUID) error {
	r.s.games[gameID].HostID = hostID
	return nil
}

func (r fakeGameRepo) UpdateStatus(_ context.Context, gameID uuid.UUID, status domain.GameStatus) error {
	g := r.s.games[gameID]
	g.Status = status
	if status == domain.GameStatusInProgress {
		started := r.s.tick()
		g.StartedAt = &started
	}
	return nil
}

// sequenceRandom returns its values in order, then repeats the last one.
type sequenceRandom struct {
	values []int
	calls  int
}

func (r *sequenceRandom) IntN(n int) int {
	v := r.values[min(r.calls, len(r.values)-1)]
	r.calls++
	return v % n
}

var validSettingsInput = GameSettingsInput{
	Rounds:       5,
	TimePerRound: 90,
	Difficulty:   "medium",
	Category:     "coding",
	AIModel:      "claude-sonnet-5",
	MaxPlayers:   4,
}

var validSettings = domain.GameSettings{
	Rounds:              5,
	TimePerRoundSeconds: 90,
	Difficulty:          domain.DifficultyMedium,
	Category:            domain.CategoryCoding,
	AIModel:             domain.AIModelClaudeSonnet5,
	MaxPlayers:          4,
}

// recordingSyncer records the games mirrored into live state.
type recordingSyncer struct {
	mu    sync.Mutex
	games []*domain.Game
}

func (r *recordingSyncer) SyncGame(_ context.Context, g *domain.Game) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.games = append(r.games, cloneGame(g))
}

func (r *recordingSyncer) synced() []*domain.Game {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.games)
}

type gameServiceFixture struct {
	store  *fakeGameStore
	random *sequenceRandom
	live   *recordingSyncer
	svc    *GameService
}

func newGameServiceFixture() *gameServiceFixture {
	f := &gameServiceFixture{
		store:  newFakeGameStore(),
		random: &sequenceRandom{values: []int{482913}},
		live:   &recordingSyncer{},
	}
	f.svc = NewGameService(f.store, f.random, f.live)
	return f
}

func settingsWith(mutate func(*domain.GameSettings)) domain.GameSettings {
	s := validSettings
	mutate(&s)
	return s
}

func assertValidationFields(t *testing.T, err error, want ...string) {
	t.Helper()
	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("error = %v, want *ValidationError", err)
	}
	got := slices.Sorted(maps.Keys(vErr.Fields))
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("invalid fields = %v, want %v (%v)", got, want, vErr.Fields)
	}
}

func TestParseGameSettings(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*GameSettingsInput)
		wantFields []string
	}{
		{name: "valid", mutate: func(*GameSettingsInput) {}},
		{name: "strings are trimmed and case-insensitive", mutate: func(in *GameSettingsInput) {
			in.Difficulty, in.Category, in.AIModel = " MEDIUM ", "Coding", "Claude-Sonnet-5"
		}},
		{name: "every allowed rounds value", mutate: func(in *GameSettingsInput) { in.Rounds = 7 }},
		{name: "rounds not allowed", mutate: func(in *GameSettingsInput) { in.Rounds = 4 }, wantFields: []string{"rounds"}},
		{name: "time per round not allowed", mutate: func(in *GameSettingsInput) { in.TimePerRound = 30 }, wantFields: []string{"time_per_round"}},
		{name: "unknown difficulty", mutate: func(in *GameSettingsInput) { in.Difficulty = "insane" }, wantFields: []string{"difficulty"}},
		{name: "unknown category", mutate: func(in *GameSettingsInput) { in.Category = "cooking" }, wantFields: []string{"category"}},
		{name: "unknown ai model", mutate: func(in *GameSettingsInput) { in.AIModel = "gpt-4" }, wantFields: []string{"ai_model"}},
		{name: "max players above 6", mutate: func(in *GameSettingsInput) { in.MaxPlayers = 7 }, wantFields: []string{"max_players"}},
		{name: "max players below 2", mutate: func(in *GameSettingsInput) { in.MaxPlayers = 1 }, wantFields: []string{"max_players"}},
		{
			name:       "missing everything",
			mutate:     func(in *GameSettingsInput) { *in = GameSettingsInput{} },
			wantFields: []string{"rounds", "time_per_round", "difficulty", "category", "ai_model", "max_players"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := validSettingsInput
			tt.mutate(&in)

			got, err := parseGameSettings(in)
			if tt.wantFields != nil {
				assertValidationFields(t, err, tt.wantFields...)
				return
			}
			if err != nil {
				t.Fatalf("parseGameSettings() error = %v", err)
			}
			if got.Difficulty != domain.DifficultyMedium || got.Category != domain.CategoryCoding || got.AIModel != domain.AIModelClaudeSonnet5 {
				t.Errorf("parseGameSettings() = %+v, want normalized values", got)
			}
		})
	}
}

func TestGameServiceCreate(t *testing.T) {
	f := newGameServiceFixture()
	f.random.values = []int{42}
	host := uuid.New()

	g, err := f.svc.Create(context.Background(), host, validSettingsInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	if g.RoomCode != "000042" {
		t.Errorf("RoomCode = %q, want zero-padded %q", g.RoomCode, "000042")
	}
	if !domain.IsRoomCode(g.RoomCode) {
		t.Errorf("RoomCode %q is not six digits", g.RoomCode)
	}
	if g.HostID != host || g.Status != domain.GameStatusWaiting {
		t.Errorf("game = %+v, want waiting game hosted by the creator", g)
	}
	if g.PlayerCount != 1 || !g.HasPlayer(host) {
		t.Errorf("players = %+v, want only the host", g.Players)
	}
	if g.Settings != validSettings {
		t.Errorf("Settings = %+v, want %+v", g.Settings, validSettings)
	}
}

func TestGameServiceCreateRetriesRoomCodeCollisions(t *testing.T) {
	f := newGameServiceFixture()
	other := uuid.New()
	f.store.seed(domain.GameStatusWaiting, "111111", validSettings, other)
	f.store.seed(domain.GameStatusInProgress, "222222", validSettings, other)
	// A finished game's code is free again.
	f.store.seed(domain.GameStatusFinished, "333333", validSettings, other)
	f.random.values = []int{111111, 222222, 333333}

	g, err := f.svc.Create(context.Background(), uuid.New(), validSettingsInput)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if g.RoomCode != "333333" {
		t.Errorf("RoomCode = %q, want %q after two collisions", g.RoomCode, "333333")
	}
	if f.random.calls != 3 {
		t.Errorf("generated %d codes, want 3", f.random.calls)
	}
}

func TestGameServiceCreateGivesUpWhenCodesStayTaken(t *testing.T) {
	f := newGameServiceFixture()
	f.store.seed(domain.GameStatusWaiting, "111111", validSettings, uuid.New())
	f.random.values = []int{111111}

	_, err := f.svc.Create(context.Background(), uuid.New(), validSettingsInput)
	if !errors.Is(err, domain.ErrRoomCodeTaken) {
		t.Fatalf("Create() error = %v, want it to wrap domain.ErrRoomCodeTaken", err)
	}
	if f.random.calls != maxRoomCodeAttempts {
		t.Errorf("generated %d codes, want %d", f.random.calls, maxRoomCodeAttempts)
	}
}

func TestGameServiceCreateRejectsInvalidSettings(t *testing.T) {
	f := newGameServiceFixture()
	in := validSettingsInput
	in.MaxPlayers = 10

	_, err := f.svc.Create(context.Background(), uuid.New(), in)
	assertValidationFields(t, err, "max_players")
	if len(f.store.games) != 0 {
		t.Error("a game was stored despite invalid settings")
	}
}

func TestGameServiceCreateRollsBackOnFailure(t *testing.T) {
	f := newGameServiceFixture()
	f.store.addPlayerErr = errors.New("connection reset")

	if _, err := f.svc.Create(context.Background(), uuid.New(), validSettingsInput); err == nil {
		t.Fatal("Create() error = nil, want the AddPlayer failure")
	}
	if len(f.store.games) != 0 {
		t.Errorf("%d games left behind after a failed create, want 0", len(f.store.games))
	}
}

func TestGameServiceGet(t *testing.T) {
	f := newGameServiceFixture()
	host := uuid.New()
	waiting := f.store.seed(domain.GameStatusWaiting, "000777", validSettings, host)
	f.store.seed(domain.GameStatusCancelled, "000888", validSettings, host)

	tests := []struct {
		name    string
		ref     string
		wantErr error
	}{
		{name: "by UUID", ref: waiting.ID.String()},
		{name: "by room code", ref: "000777"},
		{name: "unknown UUID", ref: uuid.NewString(), wantErr: domain.ErrNotFound},
		{name: "unknown room code", ref: "123456", wantErr: domain.ErrNotFound},
		{name: "room code of an inactive game", ref: "000888", wantErr: domain.ErrNotFound},
		{name: "malformed reference", ref: "not-a-game", wantErr: domain.ErrNotFound},
		{name: "five digits", ref: "00077", wantErr: domain.ErrNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := f.svc.Get(context.Background(), tt.ref)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Get(%q) error = %v, want %v", tt.ref, err, tt.wantErr)
			}
			if tt.wantErr == nil && g.ID != waiting.ID {
				t.Errorf("Get(%q) = %s, want %s", tt.ref, g.ID, waiting.ID)
			}
		})
	}
}

func TestGameServiceList(t *testing.T) {
	f := newGameServiceFixture()
	host := uuid.New()
	f.store.seed(domain.GameStatusWaiting, "100001", validSettings, host)
	f.store.seed(domain.GameStatusWaiting, "100002", validSettings, host)
	f.store.seed(domain.GameStatusInProgress, "100003", validSettings, host)

	tests := []struct {
		name       string
		status     string
		wantCount  int
		wantFields []string
	}{
		{name: "defaults to waiting", wantCount: 2},
		{name: "explicit status", status: "IN_PROGRESS", wantCount: 1},
		{name: "no matches", status: "finished", wantCount: 0},
		{name: "invalid status", status: "paused", wantFields: []string{"status"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			games, err := f.svc.List(context.Background(), tt.status)
			if tt.wantFields != nil {
				assertValidationFields(t, err, tt.wantFields...)
				return
			}
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(games) != tt.wantCount {
				t.Errorf("List(%q) returned %d games, want %d", tt.status, len(games), tt.wantCount)
			}
		})
	}
}

func TestGameServiceUpdateSettings(t *testing.T) {
	host, guest, third := uuid.New(), uuid.New(), uuid.New()
	newSettings := GameSettingsInput{Rounds: 7, TimePerRound: 120, Difficulty: "hard", Category: "reasoning", AIModel: "claude-opus-5", MaxPlayers: 6}

	tests := []struct {
		name       string
		status     domain.GameStatus
		players    []uuid.UUID
		caller     uuid.UUID
		in         GameSettingsInput
		wantErr    error
		wantFields []string
	}{
		{name: "host updates a waiting game", status: domain.GameStatusWaiting, players: []uuid.UUID{host, guest}, caller: host, in: newSettings},
		{name: "non-host is forbidden", status: domain.GameStatusWaiting, players: []uuid.UUID{host, guest}, caller: guest, in: newSettings, wantErr: domain.ErrNotGameHost},
		{name: "outsider is forbidden", status: domain.GameStatusWaiting, players: []uuid.UUID{host}, caller: uuid.New(), in: newSettings, wantErr: domain.ErrNotGameHost},
		{name: "started game cannot change", status: domain.GameStatusInProgress, players: []uuid.UUID{host, guest}, caller: host, in: newSettings, wantErr: domain.ErrGameNotWaiting},
		{name: "cancelled game cannot change", status: domain.GameStatusCancelled, players: []uuid.UUID{host}, caller: host, in: newSettings, wantErr: domain.ErrGameNotWaiting},
		{
			name: "max players below current player count", status: domain.GameStatusWaiting,
			players: []uuid.UUID{host, guest, third}, caller: host,
			in:         func() GameSettingsInput { in := newSettings; in.MaxPlayers = 2; return in }(),
			wantFields: []string{"max_players"},
		},
		{
			name: "max players equal to current player count", status: domain.GameStatusWaiting,
			players: []uuid.UUID{host, guest}, caller: host,
			in: func() GameSettingsInput { in := newSettings; in.MaxPlayers = 2; return in }(),
		},
		{
			name: "invalid settings", status: domain.GameStatusWaiting, players: []uuid.UUID{host}, caller: host,
			in:         func() GameSettingsInput { in := newSettings; in.AIModel = "gpt-4"; return in }(),
			wantFields: []string{"ai_model"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			g := f.store.seed(tt.status, "123123", validSettings, tt.players...)

			got, err := f.svc.UpdateSettings(context.Background(), tt.caller, g.ID.String(), tt.in)

			switch {
			case tt.wantFields != nil:
				assertValidationFields(t, err, tt.wantFields...)
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("UpdateSettings() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("UpdateSettings() error = %v", err)
				}
				if got.Settings.Rounds != tt.in.Rounds || got.Settings.MaxPlayers != tt.in.MaxPlayers || got.Settings.AIModel != domain.AIModel(tt.in.AIModel) {
					t.Errorf("Settings = %+v, want %+v", got.Settings, tt.in)
				}
				return
			}
			if stored := f.store.get(g.ID); stored.Settings != validSettings {
				t.Errorf("settings changed despite the error: %+v", stored.Settings)
			}
		})
	}
}

func TestGameServiceJoin(t *testing.T) {
	host, joiner := uuid.New(), uuid.New()
	full := func(n int) []uuid.UUID {
		ids := []uuid.UUID{host}
		for range n - 1 {
			ids = append(ids, uuid.New())
		}
		return ids
	}

	tests := []struct {
		name       string
		status     domain.GameStatus
		maxPlayers int
		players    []uuid.UUID
		caller     uuid.UUID // defaults to joiner
		ref        func(g *domain.Game) string
		wantErr    error
	}{
		{name: "join by UUID", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host}},
		{name: "join by room code", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host}, ref: func(g *domain.Game) string { return g.RoomCode }},
		{name: "last free seat", status: domain.GameStatusWaiting, maxPlayers: 6, players: full(5)},
		{name: "unknown game", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host}, ref: func(*domain.Game) string { return uuid.NewString() }, wantErr: domain.ErrNotFound},
		{name: "unknown room code", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host}, ref: func(*domain.Game) string { return "999999" }, wantErr: domain.ErrNotFound},
		{name: "already started", status: domain.GameStatusInProgress, maxPlayers: 6, players: []uuid.UUID{host}, wantErr: domain.ErrGameNotWaiting},
		{name: "cancelled", status: domain.GameStatusCancelled, maxPlayers: 6, players: []uuid.UUID{host}, wantErr: domain.ErrGameNotWaiting},
		{name: "7th player in a room of 6", status: domain.GameStatusWaiting, maxPlayers: 6, players: full(6), wantErr: domain.ErrGameFull},
		{name: "room smaller than 6 is full", status: domain.GameStatusWaiting, maxPlayers: 2, players: full(2), wantErr: domain.ErrGameFull},
		{name: "already joined", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host, joiner}, wantErr: domain.ErrAlreadyJoined},
		{name: "host cannot join own game", status: domain.GameStatusWaiting, maxPlayers: 6, players: []uuid.UUID{host}, caller: host, wantErr: domain.ErrAlreadyJoined},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			settings := settingsWith(func(s *domain.GameSettings) { s.MaxPlayers = tt.maxPlayers })
			g := f.store.seed(tt.status, "654321", settings, tt.players...)
			ref := g.ID.String()
			if tt.ref != nil {
				ref = tt.ref(g)
			}
			caller := joiner
			if tt.caller != uuid.Nil {
				caller = tt.caller
			}

			got, err := f.svc.Join(context.Background(), caller, ref)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Join() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if f.store.get(g.ID).PlayerCount != len(tt.players) {
					t.Error("player count changed despite the error")
				}
				return
			}
			if !got.HasPlayer(joiner) || got.PlayerCount != len(tt.players)+1 {
				t.Errorf("after Join() players = %+v, want the joiner added", got.Players)
			}
		})
	}
}

func TestGameServiceConcurrentJoinsNeverOverfill(t *testing.T) {
	f := newGameServiceFixture()
	settings := settingsWith(func(s *domain.GameSettings) { s.MaxPlayers = domain.MaxPlayers })
	g := f.store.seed(domain.GameStatusWaiting, "246810", settings, uuid.New())

	const joiners = 50
	var wg sync.WaitGroup
	errs := make(chan error, joiners)
	for range joiners {
		wg.Go(func() {
			_, err := f.svc.Join(context.Background(), uuid.New(), g.ID.String())
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	var joined, full int
	for err := range errs {
		switch {
		case err == nil:
			joined++
		case errors.Is(err, domain.ErrGameFull):
			full++
		default:
			t.Errorf("unexpected Join() error = %v", err)
		}
	}
	if joined != domain.MaxPlayers-1 || full != joiners-joined {
		t.Errorf("joined = %d, full = %d; want %d joined and the rest rejected", joined, full, domain.MaxPlayers-1)
	}
	if n := f.store.get(g.ID).PlayerCount; n != domain.MaxPlayers {
		t.Errorf("player count = %d, want %d", n, domain.MaxPlayers)
	}
}

func TestGameServiceLeave(t *testing.T) {
	host, first, second := uuid.New(), uuid.New(), uuid.New()

	tests := []struct {
		name        string
		status      domain.GameStatus
		players     []uuid.UUID
		caller      uuid.UUID
		wantErr     error
		wantStatus  domain.GameStatus
		wantHost    uuid.UUID
		wantPlayers []uuid.UUID
	}{
		{
			name: "player leaves", status: domain.GameStatusWaiting,
			players: []uuid.UUID{host, first, second}, caller: second,
			wantStatus: domain.GameStatusWaiting, wantHost: host, wantPlayers: []uuid.UUID{host, first},
		},
		{
			name: "host leaves and hosting passes to the earliest joiner", status: domain.GameStatusWaiting,
			players: []uuid.UUID{host, first, second}, caller: host,
			wantStatus: domain.GameStatusWaiting, wantHost: first, wantPlayers: []uuid.UUID{first, second},
		},
		{
			name: "last player leaves and the game is cancelled", status: domain.GameStatusWaiting,
			players: []uuid.UUID{host}, caller: host,
			wantStatus: domain.GameStatusCancelled, wantHost: host, wantPlayers: []uuid.UUID{},
		},
		{name: "outsider cannot leave", status: domain.GameStatusWaiting, players: []uuid.UUID{host}, caller: first, wantErr: domain.ErrNotInGame},
		{name: "cannot leave a started game", status: domain.GameStatusInProgress, players: []uuid.UUID{host, first}, caller: first, wantErr: domain.ErrGameNotWaiting},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			g := f.store.seed(tt.status, "135791", validSettings, tt.players...)

			err := f.svc.Leave(context.Background(), tt.caller, g.ID.String())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Leave() error = %v, want %v", err, tt.wantErr)
			}

			got := f.store.get(g.ID)
			if tt.wantErr != nil {
				if got.PlayerCount != len(tt.players) || got.Status != tt.status {
					t.Error("game changed despite the error")
				}
				return
			}
			if got.Status != tt.wantStatus || got.HostID != tt.wantHost {
				t.Errorf("status = %s, host = %s; want %s, %s", got.Status, got.HostID, tt.wantStatus, tt.wantHost)
			}
			ids := []uuid.UUID{}
			for _, p := range got.Players {
				ids = append(ids, p.UserID)
			}
			if !slices.Equal(ids, tt.wantPlayers) {
				t.Errorf("players = %v, want %v", ids, tt.wantPlayers)
			}
		})
	}
}

func TestGameServiceStart(t *testing.T) {
	host, guest := uuid.New(), uuid.New()

	tests := []struct {
		name                string
		status              domain.GameStatus
		players             []uuid.UUID
		caller              uuid.UUID
		noChallenges        bool
		wantErr             error
		wantChallengeLookup bool
	}{
		{name: "host starts a ready game", status: domain.GameStatusWaiting, players: []uuid.UUID{host, guest}, caller: host, wantChallengeLookup: true},
		{name: "non-host is forbidden", status: domain.GameStatusWaiting, players: []uuid.UUID{host, guest}, caller: guest, wantErr: domain.ErrNotGameHost},
		{name: "already started", status: domain.GameStatusInProgress, players: []uuid.UUID{host, guest}, caller: host, wantErr: domain.ErrGameNotWaiting},
		{name: "cancelled", status: domain.GameStatusCancelled, players: []uuid.UUID{host, guest}, caller: host, wantErr: domain.ErrGameNotWaiting},
		{name: "not enough players", status: domain.GameStatusWaiting, players: []uuid.UUID{host}, caller: host, wantErr: domain.ErrNotEnoughPlayers},
		{
			name: "no eligible challenge", status: domain.GameStatusWaiting, players: []uuid.UUID{host, guest}, caller: host,
			noChallenges: true, wantErr: domain.ErrNoEligibleChallenge, wantChallengeLookup: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			if tt.noChallenges {
				f.store.challenges.challenges = nil
			}
			g := f.store.seed(tt.status, "975310", validSettings, tt.players...)

			got, err := f.svc.Start(context.Background(), tt.caller, g.ID.String())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, tt.wantErr)
			}

			if looked := f.store.challenges.listCalls > 0; looked != tt.wantChallengeLookup {
				t.Errorf("challenges looked up = %v, want %v", looked, tt.wantChallengeLookup)
			}
			if tt.wantChallengeLookup {
				filter := f.store.challenges.lastFilter
				if filter.Category == nil || *filter.Category != validSettings.Category ||
					filter.Difficulty == nil || *filter.Difficulty != validSettings.Difficulty {
					t.Errorf("challenge filter = %+v, want the game's category and difficulty", filter)
				}
			}

			if tt.wantErr != nil {
				if s := f.store.get(g.ID).Status; s != tt.status {
					t.Errorf("status = %s after a failed start, want %s", s, tt.status)
				}
				return
			}
			if got.Status != domain.GameStatusInProgress || got.StartedAt == nil {
				t.Errorf("status = %s, started_at = %v; want in_progress with a start time", got.Status, got.StartedAt)
			}
		})
	}
}

func TestGameServiceCancel(t *testing.T) {
	host, guest := uuid.New(), uuid.New()

	tests := []struct {
		name    string
		status  domain.GameStatus
		caller  uuid.UUID
		wantErr error
	}{
		{name: "host cancels a waiting game", status: domain.GameStatusWaiting, caller: host},
		{name: "host cancels an in-progress game", status: domain.GameStatusInProgress, caller: host},
		{name: "non-host is forbidden", status: domain.GameStatusWaiting, caller: guest, wantErr: domain.ErrNotGameHost},
		{name: "finished game", status: domain.GameStatusFinished, caller: host, wantErr: domain.ErrGameNotCancellable},
		{name: "already cancelled", status: domain.GameStatusCancelled, caller: host, wantErr: domain.ErrGameNotCancellable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			g := f.store.seed(tt.status, "864200", validSettings, host, guest)

			err := f.svc.Cancel(context.Background(), tt.caller, g.ID.String())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Cancel() error = %v, want %v", err, tt.wantErr)
			}

			want := domain.GameStatusCancelled
			if tt.wantErr != nil {
				want = tt.status
			}
			if s := f.store.get(g.ID).Status; s != want {
				t.Errorf("status = %s, want %s", s, want)
			}
		})
	}
}

func TestGameServiceMirrorsCommittedChangesIntoLiveState(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	ctx := context.Background()

	tests := []struct {
		name       string
		run        func(f *gameServiceFixture, g *domain.Game) error
		wantStatus domain.GameStatus
		wantHost   uuid.UUID
		wantCount  int
	}{
		{
			name: "join", wantStatus: domain.GameStatusWaiting, wantHost: host, wantCount: 2,
			run: func(f *gameServiceFixture, g *domain.Game) error {
				_, err := f.svc.Join(ctx, guest, g.ID.String())
				return err
			},
		},
		{
			name: "settings", wantStatus: domain.GameStatusWaiting, wantHost: host, wantCount: 1,
			run: func(f *gameServiceFixture, g *domain.Game) error {
				_, err := f.svc.UpdateSettings(ctx, host, g.ID.String(), validSettingsInput)
				return err
			},
		},
		{
			name: "host leaves", wantStatus: domain.GameStatusCancelled, wantHost: host, wantCount: 0,
			run: func(f *gameServiceFixture, g *domain.Game) error { return f.svc.Leave(ctx, host, g.ID.String()) },
		},
		{
			name: "cancel", wantStatus: domain.GameStatusCancelled, wantHost: host, wantCount: 1,
			run: func(f *gameServiceFixture, g *domain.Game) error { return f.svc.Cancel(ctx, host, g.ID.String()) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newGameServiceFixture()
			g := f.store.seed(domain.GameStatusWaiting, "112233", validSettings, host)

			if err := tt.run(f, g); err != nil {
				t.Fatalf("error = %v", err)
			}
			synced := f.live.synced()
			if len(synced) != 1 {
				t.Fatalf("synced %d games, want 1", len(synced))
			}
			if s := synced[0]; s.ID != g.ID || s.Status != tt.wantStatus || s.HostID != tt.wantHost || s.PlayerCount != tt.wantCount {
				t.Errorf("synced %+v, want status %s, host %s, %d players", s, tt.wantStatus, tt.wantHost, tt.wantCount)
			}
		})
	}

	t.Run("create and start", func(t *testing.T) {
		f := newGameServiceFixture()
		g, err := f.svc.Create(ctx, host, validSettingsInput)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Join(ctx, guest, g.ID.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.Start(ctx, host, g.ID.String()); err != nil {
			t.Fatal(err)
		}
		statuses := []domain.GameStatus{}
		for _, s := range f.live.synced() {
			statuses = append(statuses, s.Status)
		}
		want := []domain.GameStatus{domain.GameStatusWaiting, domain.GameStatusWaiting, domain.GameStatusInProgress}
		if !slices.Equal(statuses, want) {
			t.Errorf("synced statuses = %v, want %v", statuses, want)
		}
	})

	t.Run("nothing is mirrored when the change fails", func(t *testing.T) {
		f := newGameServiceFixture()
		g := f.store.seed(domain.GameStatusWaiting, "445566", validSettings, host)
		if _, err := f.svc.Start(ctx, guest, g.ID.String()); !errors.Is(err, domain.ErrNotGameHost) {
			t.Fatalf("Start() error = %v, want ErrNotGameHost", err)
		}
		if _, err := f.svc.Create(ctx, host, GameSettingsInput{}); err == nil {
			t.Fatal("Create() with invalid settings succeeded")
		}
		if n := len(f.live.synced()); n != 0 {
			t.Errorf("synced %d games after failures, want 0", n)
		}
	})
}
