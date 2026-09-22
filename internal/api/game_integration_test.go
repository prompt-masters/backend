package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/api"
	"github.com/prompt-masters/backend/internal/api/handler"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/repository/postgres"
	"github.com/prompt-masters/backend/internal/repository/redisstore"
	"github.com/prompt-masters/backend/internal/service"
	"github.com/prompt-masters/backend/internal/testutil/pgtest"
	"github.com/prompt-masters/backend/internal/util"
	"github.com/prompt-masters/backend/internal/ws"
	"github.com/redis/go-redis/v9"
)

const jwtSecret = "integration-test-secret"

// testAPI is the real server wired to a migrated throwaway database. Only
// coding/medium challenges exist, so games configured for anything else
// cannot start.
type testAPI struct {
	t      *testing.T
	server *httptest.Server
	users  *postgres.UserRepository
	// live and logs are set when the API is built with Redis.
	live *service.LiveStateService
	logs *syncBuffer
	hub  *ws.Hub
}

// newTestAPI builds the API without live state: lobby changes are not
// mirrored anywhere.
func newTestAPI(t *testing.T) *testAPI {
	return buildTestAPI(t, nil, "")
}

// buildTestAPI builds the API. With a Redis client, live state is stored under
// keyPrefix and lobby changes are mirrored into it.
func buildTestAPI(t *testing.T, redisClient redis.UniversalClient, keyPrefix string) *testAPI {
	t.Helper()
	pool := pgtest.NewPool(t)
	queries := db.New(pool)
	ctx := context.Background()

	userRepo := postgres.NewUserRepository(queries)
	challengeRepo := postgres.NewChallengeRepository(queries)
	for i := range 2 {
		_, err := challengeRepo.Upsert(ctx, &domain.Challenge{
			Slug:        fmt.Sprintf("coding-medium-%d", i),
			Title:       "Challenge",
			Description: "Description",
			Category:    domain.CategoryCoding,
			Difficulty:  domain.DifficultyMedium,
			JudgeCriteria: domain.JudgeCriteria{PassingScore: 50, Criteria: []domain.JudgeCriterion{
				{Name: "quality", Description: "Good.", Weight: 1, MaxScore: 10},
			}},
		})
		if err != nil {
			t.Fatalf("seeding challenge: %v", err)
		}
	}

	logs := &syncBuffer{}
	logger := log.New(logs, "", 0)
	authService := service.NewAuthService(
		userRepo,
		postgres.NewVerificationTokenRepository(queries),
		postgres.NewRefreshTokenRepository(queries),
		mail.NewLogSender(io.Discard),
		config.Config{JWTSecret: jwtSecret, JWTTTL: time.Hour},
	)
	challengeService := service.NewChallengeService(challengeRepo)
	transactor := postgres.NewTransactor(pool)

	hub, err := ws.NewHub(testHubConfig(), nil, logger)
	if err != nil {
		t.Fatalf("creating hub: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := hub.Shutdown(shutdownCtx); err != nil {
			t.Errorf("hub shutdown: %v", err)
		}
	})

	var (
		live   *service.LiveStateService
		syncer service.LiveStateSyncer = noopSyncer{}
	)
	if redisClient != nil {
		store, err := redisstore.New(redisClient, redisstore.Options{
			KeyPrefix: keyPrefix,
			ActiveTTL: 2 * time.Hour,
			EndedTTL:  15 * time.Minute,
			OpTimeout: 500 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("creating game state store: %v", err)
		}
		live = service.NewLiveStateService(store, transactor, logger, time.Now)
		syncer = live
		hub.SetRouter(service.NewWSRouter(live, hub, logger))
	}
	gameService := service.NewGameService(transactor, service.MathRandom{}, syncer, service.NewGameEvents(hub, logger))
	websocketHandler := handler.NewWebSocketHandler(hub, live, jwtSecret, []string{"https://app.example.com"}, logger)

	server := httptest.NewServer(api.NewServer(authService, challengeService, gameService, live, websocketHandler, nil, logger, jwtSecret).Routes())
	t.Cleanup(server.Close)
	return &testAPI{t: t, server: server, users: userRepo, live: live, logs: logs, hub: hub}
}

// testHubConfig keeps deadlines generous: test clients idle between
// assertions and only answer pings while reading.
func testHubConfig() ws.Config {
	cfg := ws.DefaultConfig()
	cfg.PingInterval = 2 * time.Second
	cfg.PongWait = 30 * time.Second
	// Small buffer and a short write deadline so back pressure shows up
	// quickly in tests.
	cfg.SendBuffer = 8
	cfg.WriteWait = 500 * time.Millisecond
	return cfg
}

// syncBuffer is a log destination safe to read while handlers write to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type testUser struct {
	ID    uuid.UUID
	Token string
}

func (a *testAPI) user(name string) testUser {
	a.t.Helper()
	u, err := a.users.Create(context.Background(), &domain.User{Username: name, Email: name + "@example.com", Password: "hash"})
	if err != nil {
		a.t.Fatalf("creating user %q: %v", name, err)
	}
	token, err := util.GenerateJWT(jwtSecret, u.ID, time.Hour)
	if err != nil {
		a.t.Fatalf("signing token: %v", err)
	}
	return testUser{ID: u.ID, Token: token}
}

type apiResponse struct {
	Status int
	Body   []byte
}

func (r apiResponse) String() string { return fmt.Sprintf("%d %s", r.Status, r.Body) }

// call sends a request; it is safe for concurrent use.
func (a *testAPI) call(method, path string, u *testUser, body any) (apiResponse, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return apiResponse{}, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.server.URL+path, reader)
	if err != nil {
		return apiResponse{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if u != nil {
		req.Header.Set("Authorization", "Bearer "+u.Token)
	}
	resp, err := a.server.Client().Do(req)
	if err != nil {
		return apiResponse{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return apiResponse{Status: resp.StatusCode, Body: raw}, err
}

func (a *testAPI) do(method, path string, u *testUser, body any) apiResponse {
	a.t.Helper()
	resp, err := a.call(method, path, u, body)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (a *testAPI) expect(want int, method, path string, u *testUser, body any) apiResponse {
	a.t.Helper()
	resp := a.do(method, path, u, body)
	if resp.Status != want {
		a.t.Fatalf("%s %s = %s, want status %d", method, path, resp, want)
	}
	return resp
}

type gameJSON struct {
	ID          string `json:"id"`
	RoomCode    string `json:"room_code"`
	HostID      string `json:"host_id"`
	Status      string `json:"status"`
	PlayerCount int    `json:"player_count"`
	StartedAt   *time.Time
	Settings    struct {
		Rounds       int    `json:"rounds"`
		TimePerRound int    `json:"time_per_round"`
		Difficulty   string `json:"difficulty"`
		Category     string `json:"category"`
		AIModel      string `json:"ai_model"`
		MaxPlayers   int    `json:"max_players"`
	} `json:"settings"`
	Players []struct {
		UserID   string `json:"user_id"`
		Username string `json:"username"`
		IsHost   bool   `json:"is_host"`
	} `json:"players"`
}

func decode[T any](t *testing.T, resp apiResponse) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(resp.Body, &envelope); err != nil {
		t.Fatalf("decoding %s: %v", resp, err)
	}
	return envelope.Data
}

func decodeFields(t *testing.T, resp apiResponse) map[string]string {
	t.Helper()
	var body struct {
		Fields map[string]string `json:"fields"`
	}
	if err := json.Unmarshal(resp.Body, &body); err != nil {
		t.Fatalf("decoding %s: %v", resp, err)
	}
	return body.Fields
}

func settings(maxPlayers int) map[string]any {
	return map[string]any{
		"rounds":         5,
		"time_per_round": 90,
		"difficulty":     "medium",
		"category":       "coding",
		"ai_model":       "claude-sonnet-5",
		"max_players":    maxPlayers,
	}
}

// createGame creates a game hosted by host and joins the guests to it.
func (a *testAPI) createGame(host testUser, maxPlayers int, guests ...testUser) gameJSON {
	a.t.Helper()
	g := decode[gameJSON](a.t, a.expect(http.StatusCreated, "POST", "/api/v1/games", &host, settings(maxPlayers)))
	for _, guest := range guests {
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/join", &guest, nil)
	}
	return g
}

func (a *testAPI) getGame(u testUser, ref string) gameJSON {
	a.t.Helper()
	return decode[gameJSON](a.t, a.expect(http.StatusOK, "GET", "/api/v1/games/"+ref, &u, nil))
}

func TestGameEndpointsRequireAuthentication(t *testing.T) {
	a := newTestAPI(t)
	id := uuid.NewString()
	invalid := testUser{Token: "not-a-jwt"}

	routes := []struct{ method, path string }{
		{"GET", "/api/v1/games"},
		{"POST", "/api/v1/games"},
		{"GET", "/api/v1/games/" + id},
		{"PUT", "/api/v1/games/" + id + "/settings"},
		{"POST", "/api/v1/games/" + id + "/join"},
		{"POST", "/api/v1/games/" + id + "/leave"},
		{"POST", "/api/v1/games/" + id + "/start"},
		{"DELETE", "/api/v1/games/" + id},
	}
	for _, r := range routes {
		t.Run(r.method+" "+r.path, func(t *testing.T) {
			if resp := a.do(r.method, r.path, nil, nil); resp.Status != http.StatusUnauthorized {
				t.Errorf("without token = %s, want 401", resp)
			}
			if resp := a.do(r.method, r.path, &invalid, nil); resp.Status != http.StatusUnauthorized {
				t.Errorf("with invalid token = %s, want 401", resp)
			}
		})
	}
}

func TestCreateGame(t *testing.T) {
	a := newTestAPI(t)
	host := a.user("host")

	t.Run("creator becomes host and first player", func(t *testing.T) {
		g := decode[gameJSON](t, a.expect(http.StatusCreated, "POST", "/api/v1/games", &host, settings(4)))

		if !domain.IsRoomCode(g.RoomCode) {
			t.Errorf("room_code = %q, want exactly 6 digits", g.RoomCode)
		}
		if g.Status != "waiting" || g.HostID != host.ID.String() {
			t.Errorf("game = %+v, want waiting and hosted by the creator", g)
		}
		if g.PlayerCount != 1 || len(g.Players) != 1 || g.Players[0].UserID != host.ID.String() || !g.Players[0].IsHost {
			t.Errorf("players = %+v, want only the host", g.Players)
		}
		s := g.Settings
		if s.Rounds != 5 || s.TimePerRound != 90 || s.Difficulty != "medium" || s.Category != "coding" || s.AIModel != "claude-sonnet-5" || s.MaxPlayers != 4 {
			t.Errorf("settings = %+v, want the requested settings", s)
		}
	})

	t.Run("room codes are unique", func(t *testing.T) {
		codes := map[string]bool{}
		for range 25 {
			g := decode[gameJSON](t, a.expect(http.StatusCreated, "POST", "/api/v1/games", &host, settings(4)))
			if codes[g.RoomCode] {
				t.Fatalf("room code %s issued twice", g.RoomCode)
			}
			codes[g.RoomCode] = true
		}
	})

	t.Run("invalid settings", func(t *testing.T) {
		body := map[string]any{"rounds": 4, "time_per_round": 30, "difficulty": "insane", "category": "cooking", "ai_model": "gpt-4", "max_players": 7}
		resp := a.expect(http.StatusBadRequest, "POST", "/api/v1/games", &host, body)

		fields := decodeFields(t, resp)
		for _, f := range []string{"rounds", "time_per_round", "difficulty", "category", "ai_model", "max_players"} {
			if fields[f] == "" {
				t.Errorf("fields[%q] is empty; got %v", f, fields)
			}
		}
	})

	t.Run("malformed body", func(t *testing.T) {
		a.expect(http.StatusBadRequest, "POST", "/api/v1/games", &host, map[string]any{"rounds": "five"})
	})
}

func TestGetAndListGames(t *testing.T) {
	a := newTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	waiting := a.createGame(host, 4, guest)
	started := a.createGame(host, 4, guest)
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+started.ID+"/start", &host, nil)

	t.Run("get by UUID and by room code", func(t *testing.T) {
		for _, ref := range []string{waiting.ID, waiting.RoomCode} {
			g := a.getGame(guest, ref)
			if g.ID != waiting.ID || len(g.Players) != 2 {
				t.Errorf("GET %s = %+v, want the waiting game with 2 players", ref, g)
			}
		}
	})

	t.Run("unknown or malformed references are 404", func(t *testing.T) {
		for _, ref := range []string{uuid.NewString(), "000000", "not-a-game"} {
			a.expect(http.StatusNotFound, "GET", "/api/v1/games/"+ref, &guest, nil)
		}
	})

	t.Run("list defaults to waiting games", func(t *testing.T) {
		games := decode[[]gameJSON](t, a.expect(http.StatusOK, "GET", "/api/v1/games", &guest, nil))
		if len(games) != 1 || games[0].ID != waiting.ID || games[0].PlayerCount != 2 {
			t.Errorf("GET /games = %+v, want only the waiting game with 2 players", games)
		}
	})

	t.Run("list by status", func(t *testing.T) {
		games := decode[[]gameJSON](t, a.expect(http.StatusOK, "GET", "/api/v1/games?status=in_progress", &guest, nil))
		if len(games) != 1 || games[0].ID != started.ID {
			t.Errorf("GET /games?status=in_progress = %+v, want the started game", games)
		}
		a.expect(http.StatusBadRequest, "GET", "/api/v1/games?status=paused", &guest, nil)
	})
}

func TestUpdateGameSettings(t *testing.T) {
	a := newTestAPI(t)
	host, guest, third := a.user("host"), a.user("guest"), a.user("third")
	g := a.createGame(host, 4, guest, third)
	path := "/api/v1/games/" + g.ID + "/settings"

	updated := map[string]any{"rounds": 7, "time_per_round": 120, "difficulty": "medium", "category": "coding", "ai_model": "claude-opus-5", "max_players": 6}

	t.Run("non-host is forbidden", func(t *testing.T) {
		a.expect(http.StatusForbidden, "PUT", path, &guest, updated)
	})

	t.Run("max players below player count", func(t *testing.T) {
		resp := a.expect(http.StatusBadRequest, "PUT", path, &host, settings(2))
		if decodeFields(t, resp)["max_players"] == "" {
			t.Errorf("response %s does not flag max_players", resp)
		}
	})

	t.Run("host updates a waiting game", func(t *testing.T) {
		got := decode[gameJSON](t, a.expect(http.StatusOK, "PUT", path, &host, updated))
		if got.Settings.Rounds != 7 || got.Settings.TimePerRound != 120 || got.Settings.AIModel != "claude-opus-5" || got.Settings.MaxPlayers != 6 {
			t.Errorf("settings = %+v, want the update applied", got.Settings)
		}
	})

	t.Run("settings cannot change after start", func(t *testing.T) {
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		a.expect(http.StatusConflict, "PUT", path, &host, settings(4))
		if s := a.getGame(host, g.ID).Settings; s.Rounds != 7 {
			t.Errorf("settings = %+v, want them unchanged after start", s)
		}
	})
}

func TestJoinGame(t *testing.T) {
	a := newTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 6)

	t.Run("join by room code", func(t *testing.T) {
		got := decode[gameJSON](t, a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.RoomCode+"/join", &guest, nil))
		if got.PlayerCount != 2 || got.Players[1].UserID != guest.ID.String() || got.Players[1].IsHost {
			t.Errorf("players = %+v, want host then guest", got.Players)
		}
	})

	t.Run("cannot join twice", func(t *testing.T) {
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/join", &guest, nil)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/join", &host, nil)
	})

	t.Run("invalid rooms", func(t *testing.T) {
		for _, ref := range []string{uuid.NewString(), "999999", "12345", "abc"} {
			a.expect(http.StatusNotFound, "POST", "/api/v1/games/"+ref+"/join", &guest, nil)
		}
	})

	t.Run("7th player cannot join a room of 6", func(t *testing.T) {
		for i := range 4 {
			u := a.user(fmt.Sprintf("player%d", i))
			a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/join", &u, nil)
		}
		seventh := a.user("seventh")
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/join", &seventh, nil)
		if n := a.getGame(host, g.ID).PlayerCount; n != 6 {
			t.Errorf("player_count = %d, want 6", n)
		}
	})

	t.Run("cannot join a started game", func(t *testing.T) {
		started := a.createGame(host, 4, guest)
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+started.ID+"/start", &host, nil)
		late := a.user("late")
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+started.ID+"/join", &late, nil)
	})
}

func TestLeaveGame(t *testing.T) {
	a := newTestAPI(t)
	host, first, second, outsider := a.user("host"), a.user("first"), a.user("second"), a.user("outsider")

	t.Run("player leaves", func(t *testing.T) {
		g := a.createGame(host, 4, first)
		a.expect(http.StatusNoContent, "POST", "/api/v1/games/"+g.ID+"/leave", &first, nil)
		if got := a.getGame(host, g.ID); got.PlayerCount != 1 || got.HostID != host.ID.String() {
			t.Errorf("game = %+v, want only the host left", got)
		}
	})

	t.Run("non-player cannot leave", func(t *testing.T) {
		g := a.createGame(host, 4)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/leave", &outsider, nil)
	})

	t.Run("host leaving passes hosting to the earliest joiner", func(t *testing.T) {
		g := a.createGame(host, 4, first, second)
		a.expect(http.StatusNoContent, "POST", "/api/v1/games/"+g.ID+"/leave", &host, nil)

		got := a.getGame(first, g.ID)
		if got.HostID != first.ID.String() || got.Status != "waiting" || got.PlayerCount != 2 {
			t.Fatalf("game = %+v, want first as host of a waiting game with 2 players", got)
		}
		// The new host has host powers; the old host has none.
		a.expect(http.StatusForbidden, "POST", "/api/v1/games/"+g.ID+"/start", &second, nil)
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &first, nil)
	})

	t.Run("game is cancelled when the last player leaves", func(t *testing.T) {
		g := a.createGame(host, 4)
		a.expect(http.StatusNoContent, "POST", "/api/v1/games/"+g.ID+"/leave", &host, nil)
		if got := a.getGame(host, g.ID); got.Status != "cancelled" || got.PlayerCount != 0 {
			t.Errorf("game = %+v, want cancelled with no players", got)
		}
	})

	t.Run("cannot leave a started game", func(t *testing.T) {
		g := a.createGame(host, 4, first)
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/leave", &first, nil)
	})
}

func TestStartGame(t *testing.T) {
	a := newTestAPI(t)
	host, guest := a.user("host"), a.user("guest")

	t.Run("non-host cannot start", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		a.expect(http.StatusForbidden, "POST", "/api/v1/games/"+g.ID+"/start", &guest, nil)
		if s := a.getGame(host, g.ID).Status; s != "waiting" {
			t.Errorf("status = %s, want waiting", s)
		}
	})

	t.Run("needs at least two players", func(t *testing.T) {
		g := a.createGame(host, 4)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	})

	t.Run("needs an eligible challenge", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		noChallenges := settings(4)
		noChallenges["category"] = "reasoning"
		a.expect(http.StatusOK, "PUT", "/api/v1/games/"+g.ID+"/settings", &host, noChallenges)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		if s := a.getGame(host, g.ID).Status; s != "waiting" {
			t.Errorf("status = %s, want waiting", s)
		}
	})

	t.Run("host starts a ready game", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		got := decode[gameJSON](t, a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.RoomCode+"/start", &host, nil))
		if got.Status != "in_progress" {
			t.Errorf("status = %s, want in_progress", got.Status)
		}
		if a.getGame(guest, g.ID).Status != "in_progress" {
			t.Error("status change was not persisted")
		}
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	})
}

func TestCancelGame(t *testing.T) {
	a := newTestAPI(t)
	host, guest := a.user("host"), a.user("guest")

	t.Run("non-host cannot cancel", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		a.expect(http.StatusForbidden, "DELETE", "/api/v1/games/"+g.ID, &guest, nil)
	})

	t.Run("host cancels a waiting game", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		a.expect(http.StatusNoContent, "DELETE", "/api/v1/games/"+g.ID, &host, nil)

		if got := a.getGame(host, g.ID); got.Status != "cancelled" || got.PlayerCount != 2 {
			t.Errorf("game = %+v, want it kept with status cancelled", got)
		}
		// The room code no longer resolves, and nobody can join or restart.
		a.expect(http.StatusNotFound, "GET", "/api/v1/games/"+g.RoomCode, &host, nil)
		a.expect(http.StatusConflict, "POST", "/api/v1/games/"+g.ID+"/join", ptr(a.user("late")), nil)
		a.expect(http.StatusConflict, "DELETE", "/api/v1/games/"+g.ID, &host, nil)
	})

	t.Run("host cancels an in-progress game", func(t *testing.T) {
		g := a.createGame(host, 4, guest)
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		a.expect(http.StatusNoContent, "DELETE", "/api/v1/games/"+g.ID, &host, nil)
		if s := a.getGame(host, g.ID).Status; s != "cancelled" {
			t.Errorf("status = %s, want cancelled", s)
		}
	})

	t.Run("unknown game", func(t *testing.T) {
		a.expect(http.StatusNotFound, "DELETE", "/api/v1/games/"+uuid.NewString(), &host, nil)
	})
}

func TestConcurrentJoinsNeverExceedMaxPlayers(t *testing.T) {
	a := newTestAPI(t)
	host := a.user("host")
	g := a.createGame(host, domain.MaxPlayers)

	const joiners = 40
	users := make([]testUser, joiners)
	for i := range users {
		users[i] = a.user(fmt.Sprintf("joiner%d", i))
	}

	var wg sync.WaitGroup
	statuses := make(chan int, joiners)
	start := make(chan struct{})
	for _, u := range users {
		wg.Go(func() {
			<-start
			resp, err := a.call("POST", "/api/v1/games/"+g.ID+"/join", &u, nil)
			if err != nil {
				t.Errorf("join request failed: %v", err)
				return
			}
			statuses <- resp.Status
		})
	}
	close(start)
	wg.Wait()
	close(statuses)

	counts := map[int]int{}
	for s := range statuses {
		counts[s]++
	}
	if counts[http.StatusOK] != domain.MaxPlayers-1 || counts[http.StatusConflict] != joiners-(domain.MaxPlayers-1) {
		t.Errorf("join statuses = %v, want %d OK and the rest 409", counts, domain.MaxPlayers-1)
	}
	if got := a.getGame(host, g.ID); got.PlayerCount != domain.MaxPlayers || len(got.Players) != domain.MaxPlayers {
		t.Errorf("player_count = %d, players = %d, want %d", got.PlayerCount, len(got.Players), domain.MaxPlayers)
	}
}

func TestConcurrentJoinsCannotLandAfterStart(t *testing.T) {
	a := newTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, domain.MaxPlayers, guest)

	// More joiners than pool connections: every connection can end up queued
	// on the game lock while start holds it, so start must not need another.
	const joiners = 40
	users := make([]testUser, joiners)
	for i := range users {
		users[i] = a.user(fmt.Sprintf("joiner%d", i))
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	var startResp apiResponse
	wg.Go(func() {
		<-start
		resp, err := a.call("POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
		if err != nil {
			t.Errorf("start request failed: %v", err)
		}
		startResp = resp
	})
	for _, u := range users {
		wg.Go(func() {
			<-start
			if _, err := a.call("POST", "/api/v1/games/"+g.ID+"/join", &u, nil); err != nil {
				t.Errorf("join request failed: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	if startResp.Status != http.StatusOK {
		t.Fatalf("start = %s, want 200", startResp)
	}
	atStart := decode[gameJSON](t, startResp)
	final := a.getGame(host, g.ID)
	if final.Status != "in_progress" {
		t.Errorf("status = %s, want in_progress", final.Status)
	}
	if final.PlayerCount != atStart.PlayerCount {
		t.Errorf("player_count went from %d at start to %d afterwards; a join landed after start",
			atStart.PlayerCount, final.PlayerCount)
	}
	if final.PlayerCount > domain.MaxPlayers {
		t.Errorf("player_count = %d exceeds %d", final.PlayerCount, domain.MaxPlayers)
	}
}

func ptr[T any](v T) *T { return &v }

// noopSyncer keeps the lobby tests independent of Redis.
type noopSyncer struct{}

func (noopSyncer) SyncGame(context.Context, *domain.Game) {}
