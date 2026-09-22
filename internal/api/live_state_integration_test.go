package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/testutil/redistest"
	"github.com/redis/go-redis/v9"
)

type liveTestAPI struct {
	*testAPI
	redis  *redis.Client
	prefix string
}

func newLiveTestAPI(t *testing.T) *liveTestAPI {
	t.Helper()
	client, prefix := redistest.Client(t)
	return &liveTestAPI{testAPI: buildTestAPI(t, client, prefix), redis: client, prefix: prefix}
}

type liveJSON struct {
	GameID   string `json:"game_id"`
	RoomCode string `json:"room_code"`
	HostID   string `json:"host_id"`
	Status   string `json:"status"`
	Settings struct {
		TimePerRound int `json:"time_per_round"`
	} `json:"settings"`
	Players []struct {
		UserID  string `json:"user_id"`
		IsHost  bool   `json:"is_host"`
		IsReady bool   `json:"is_ready"`
		Points  int64  `json:"points"`
	} `json:"players"`
	Round *struct {
		Number      int       `json:"number"`
		ChallengeID string    `json:"challenge_id"`
		Deadline    time.Time `json:"deadline"`
		RemainingMs int64     `json:"remaining_ms"`
	} `json:"round"`
	Draft *struct {
		Content     string `json:"content"`
		RoundNumber int    `json:"round_number"`
	} `json:"draft"`
	Rebuilt bool `json:"rebuilt"`
}

type draftJSON struct {
	Content     string `json:"content"`
	RoundNumber int    `json:"round_number"`
}

func (a *liveTestAPI) live(u testUser, ref string) liveJSON {
	a.t.Helper()
	return decode[liveJSON](a.t, a.expect(http.StatusOK, "GET", "/api/v1/games/"+ref+"/live", &u, nil))
}

func (lj liveJSON) player(id uuid.UUID) (isReady bool, points int64, found bool) {
	for _, p := range lj.Players {
		if p.UserID == id.String() {
			return p.IsReady, p.Points, true
		}
	}
	return false, 0, false
}

func TestLiveStateMirrorsLobbyChanges(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")

	g := a.createGame(host, 4)
	got := a.live(host, g.ID)
	if got.Rebuilt || got.Status != "waiting" || got.HostID != host.ID.String() || len(got.Players) != 1 {
		t.Fatalf("after create: %+v, want waiting game from Redis with the host", got)
	}

	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.RoomCode+"/join", &guest, nil)
	if got := a.live(guest, g.RoomCode); got.Rebuilt || len(got.Players) != 2 {
		t.Errorf("after join: %+v, want 2 players from Redis", got)
	}

	a.expect(http.StatusNoContent, "POST", "/api/v1/games/"+g.ID+"/leave", &host, nil)
	got = a.live(guest, g.ID)
	if got.HostID != guest.ID.String() || len(got.Players) != 1 {
		t.Errorf("after host left: %+v, want guest as the only player and host", got)
	}
	a.expect(http.StatusForbidden, "GET", "/api/v1/games/"+g.ID+"/live", &host, nil)
}

func TestLiveStateAccess(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest, outsider := a.user("host"), a.user("guest"), a.user("outsider")
	g := a.createGame(host, 4, guest)

	a.expect(http.StatusForbidden, "GET", "/api/v1/games/"+g.ID+"/live", &outsider, nil)
	a.expect(http.StatusForbidden, "PUT", "/api/v1/games/"+g.ID+"/ready", &outsider, nil)
	a.expect(http.StatusNotFound, "GET", "/api/v1/games/"+uuid.NewString()+"/live", &host, nil)
	a.expect(http.StatusNotFound, "GET", "/api/v1/games/not-a-game/live", &host, nil)

	for _, r := range []struct{ method, path string }{
		{"GET", "/live"}, {"PUT", "/ready"}, {"DELETE", "/ready"}, {"GET", "/draft"}, {"PUT", "/draft"},
	} {
		if resp := a.do(r.method, "/api/v1/games/"+g.ID+r.path, nil, nil); resp.Status != http.StatusUnauthorized {
			t.Errorf("%s %s without a token = %s, want 401", r.method, r.path, resp)
		}
	}
}

func TestLiveStateReady(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)

	a.expect(http.StatusNoContent, "PUT", "/api/v1/games/"+g.ID+"/ready", &guest, nil)
	if ready, _, _ := a.live(host, g.ID).player(guest.ID); !ready {
		t.Error("guest is not ready after PUT /ready")
	}

	a.expect(http.StatusNoContent, "DELETE", "/api/v1/games/"+g.ID+"/ready", &guest, nil)
	if ready, _, _ := a.live(host, g.ID).player(guest.ID); ready {
		t.Error("guest is still ready after DELETE /ready")
	}

	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	a.expect(http.StatusConflict, "PUT", "/api/v1/games/"+g.ID+"/ready", &guest, nil)
}

func TestLiveStateDraftsArePrivateAndRoundsServerTimed(t *testing.T) {
	a := newLiveTestAPI(t)
	alice, bob := a.user("alice"), a.user("bob")
	g := a.createGame(alice, 4, bob)
	draftPath := "/api/v1/games/" + g.ID + "/draft"

	a.expect(http.StatusConflict, "PUT", draftPath, &alice, map[string]any{"content": "too early"})

	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &alice, nil)
	gameID := uuid.MustParse(g.ID)
	challengeID := uuid.New()
	if _, err := a.testAPI.live.StartRound(context.Background(), gameID, 1, challengeID); err != nil {
		t.Fatalf("StartRound() error = %v", err)
	}

	saved := decode[draftJSON](t, a.expect(http.StatusOK, "PUT", draftPath, &alice, map[string]any{"content": "alice's secret prompt"}))
	if saved.Content != "alice's secret prompt" || saved.RoundNumber != 1 {
		t.Errorf("saved draft = %+v, want content with round 1", saved)
	}

	// Alice reads her own draft; Bob cannot, and there is no URL that names
	// another user's draft.
	if got := decode[draftJSON](t, a.expect(http.StatusOK, "GET", draftPath, &alice, nil)); got.Content != "alice's secret prompt" {
		t.Errorf("alice's draft = %+v", got)
	}
	resp := a.expect(http.StatusNotFound, "GET", draftPath, &bob, nil)
	if strings.Contains(string(resp.Body), "secret") {
		t.Errorf("bob's response leaks alice's draft: %s", resp)
	}
	a.expect(http.StatusNotFound, "GET", draftPath+"?user_id="+alice.ID.String(), &bob, nil)

	aliceLive, bobLive := a.live(alice, g.ID), a.live(bob, g.ID)
	if aliceLive.Draft == nil || aliceLive.Draft.Content != "alice's secret prompt" {
		t.Errorf("alice's live draft = %+v", aliceLive.Draft)
	}
	if bobLive.Draft != nil {
		t.Errorf("bob's live state exposes a draft: %+v", bobLive.Draft)
	}

	round := aliceLive.Round
	if round == nil || round.Number != 1 || round.ChallengeID != challengeID.String() {
		t.Fatalf("round = %+v, want round 1 with the challenge", round)
	}
	limit := int64(aliceLive.Settings.TimePerRound) * 1000
	if round.RemainingMs <= 0 || round.RemainingMs > limit {
		t.Errorf("remaining_ms = %d, want within (0, %d]", round.RemainingMs, limit)
	}

	// Invalid drafts are rejected.
	a.expect(http.StatusBadRequest, "PUT", draftPath, &alice, map[string]any{"content": "   "})
	a.expect(http.StatusBadRequest, "PUT", draftPath, &alice, map[string]any{"content": strings.Repeat("x", 9000)})
}

func TestLiveStateRoomsAreIsolated(t *testing.T) {
	a := newLiveTestAPI(t)
	alice, bob := a.user("alice"), a.user("bob")
	carol, dave := a.user("carol"), a.user("dave")

	roomA := a.createGame(alice, 4, bob)
	roomB := a.createGame(carol, 4, dave)
	for _, room := range []struct {
		g    gameJSON
		host testUser
	}{{roomA, alice}, {roomB, carol}} {
		a.expect(http.StatusOK, "POST", "/api/v1/games/"+room.g.ID+"/start", &room.host, nil)
		if _, err := a.testAPI.live.StartRound(context.Background(), uuid.MustParse(room.g.ID), 1, uuid.New()); err != nil {
			t.Fatal(err)
		}
	}
	a.expect(http.StatusOK, "PUT", "/api/v1/games/"+roomA.ID+"/draft", &alice, map[string]any{"content": "room A only"})
	if _, err := a.testAPI.live.AwardPoints(context.Background(), uuid.MustParse(roomA.ID), bob.ID, 30); err != nil {
		t.Fatal(err)
	}

	// Room A's players are refused by room B, and nothing of room A shows up.
	a.expect(http.StatusForbidden, "GET", "/api/v1/games/"+roomB.ID+"/live", &alice, nil)
	a.expect(http.StatusNotFound, "GET", "/api/v1/games/"+roomB.ID+"/draft", &alice, nil)
	a.expect(http.StatusForbidden, "PUT", "/api/v1/games/"+roomB.ID+"/draft", &alice, map[string]any{"content": "sneaky"})

	b := a.live(carol, roomB.ID)
	if b.Draft != nil || len(b.Players) != 2 {
		t.Errorf("room B live state = %+v", b)
	}
	for _, p := range b.Players {
		if p.Points != 0 || p.UserID == alice.ID.String() || p.UserID == bob.ID.String() {
			t.Errorf("room B player %+v leaks room A data", p)
		}
	}
	if _, points, _ := a.live(alice, roomA.ID).player(bob.ID); points != 30 {
		t.Errorf("room A points for bob = %d, want 30", points)
	}
}

func TestLiveStateReconnectRebuildsFromPostgres(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)
	a.expect(http.StatusNoContent, "PUT", "/api/v1/games/"+g.ID+"/ready", &guest, nil)

	// Redis loses the game (eviction, restart, expiry).
	if err := a.testAPI.live.ClearGame(context.Background(), uuid.MustParse(g.ID)); err != nil {
		t.Fatal(err)
	}

	got := a.live(guest, g.ID)
	if !got.Rebuilt || got.Status != "waiting" || got.HostID != host.ID.String() || len(got.Players) != 2 {
		t.Fatalf("after losing Redis state: %+v, want lobby and roster rebuilt from PostgreSQL", got)
	}
	if ready, _, _ := got.player(guest.ID); ready {
		t.Error("readiness survived although it only lives in Redis")
	}

	// The rebuilt state was written back, so the next read comes from Redis.
	if again := a.live(host, g.ID); again.Rebuilt || len(again.Players) != 2 {
		t.Errorf("second read = %+v, want it served from Redis", again)
	}
}

func TestLiveStateKeysFollowSchemaAndExpire(t *testing.T) {
	a := newLiveTestAPI(t)
	host, guest := a.user("host"), a.user("guest")
	g := a.createGame(host, 4, guest)
	a.expect(http.StatusNoContent, "PUT", "/api/v1/games/"+g.ID+"/ready", &guest, nil)
	a.expect(http.StatusOK, "POST", "/api/v1/games/"+g.ID+"/start", &host, nil)
	if _, err := a.testAPI.live.StartRound(context.Background(), uuid.MustParse(g.ID), 1, uuid.New()); err != nil {
		t.Fatal(err)
	}
	a.expect(http.StatusOK, "PUT", "/api/v1/games/"+g.ID+"/draft", &guest, map[string]any{"content": "x"})
	if _, err := a.testAPI.live.AwardPoints(context.Background(), uuid.MustParse(g.ID), host.ID, 5); err != nil {
		t.Fatal(err)
	}

	uuidRe := `[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`
	schema := regexp.MustCompile(`^` + regexp.QuoteMeta(a.prefix) + `:game:\{` + g.ID + `\}:(state|players|ready|round|points|draft:` + uuidRe + `)$`)

	keys := redistest.Keys(t, a.redis, a.prefix)
	if len(keys) != 6 {
		t.Errorf("keys = %v, want state, players, ready, round, points and one draft", keys)
	}
	for _, k := range keys {
		if !schema.MatchString(k) {
			t.Errorf("key %q does not follow the schema", k)
		}
		if ttl := a.redis.PTTL(context.Background(), k).Val(); ttl <= 0 || ttl > 2*time.Hour {
			t.Errorf("key %s TTL = %s, want within (0, 2h]", k, ttl)
		}
	}

	// Cancelling shortens the expiry of every key.
	a.expect(http.StatusNoContent, "DELETE", "/api/v1/games/"+g.ID, &host, nil)
	for _, k := range redistest.Keys(t, a.redis, a.prefix) {
		if ttl := a.redis.PTTL(context.Background(), k).Val(); ttl <= 0 || ttl > 15*time.Minute {
			t.Errorf("after cancel key %s TTL = %s, want within (0, 15m]", k, ttl)
		}
	}
}

func TestLiveStateRedisDownIsControlled(t *testing.T) {
	down := redis.NewClient(&redis.Options{
		Addr:                  "127.0.0.1:1", // nothing listens here
		DialTimeout:           100 * time.Millisecond,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { down.Close() })
	a := buildTestAPI(t, down, "promptgame")
	host, guest := a.user("host"), a.user("guest")

	// PostgreSQL-backed lobby operations keep working; the mirror fails quietly.
	g := a.createGame(host, 4, guest)

	for _, req := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/games/" + g.ID + "/live", nil},
		{"PUT", "/api/v1/games/" + g.ID + "/ready", nil},
		{"GET", "/api/v1/games/" + g.ID + "/draft", nil},
		{"PUT", "/api/v1/games/" + g.ID + "/draft", map[string]any{"content": "confidential draft text"}},
	} {
		resp := a.do(req.method, req.path, &host, req.body)
		if resp.Status != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %s, want 503", req.method, req.path, resp)
			continue
		}
		var body struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(resp.Body, &body); err != nil || body.Error == "" {
			t.Errorf("%s %s body = %s, want a JSON error", req.method, req.path, resp.Body)
		}
		if strings.Contains(string(resp.Body), "127.0.0.1") {
			t.Errorf("%s %s leaks connection details: %s", req.method, req.path, resp.Body)
		}
	}

	logs := a.logs.String()
	for _, want := range []string{"live_state op=sync", "unavailable=true"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "confidential draft text") {
		t.Error("draft content was logged")
	}
}
