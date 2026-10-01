package ws

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// TestEventTypesMatchTheContract pins the wire names down: changing one breaks
// every client.
func TestEventTypesMatchTheContract(t *testing.T) {
	payloads := map[string]Payload{
		"game_state":       GameStatePayload{},
		"player_joined":    PlayerJoinedPayload{},
		"player_left":      PlayerLeftPayload{},
		"player_ready":     PlayerReadyPayload{},
		"player_typing":    PlayerTypingPayload{},
		"round_start":      RoundStartPayload{},
		"timer_tick":       TimerTickPayload{},
		"output_chunk":     OutputChunkPayload{},
		"output_complete":  OutputCompletePayload{},
		"output_error":     OutputErrorPayload{},
		"player_submitted": PlayerSubmittedPayload{},
		"round_end":        RoundEndPayload{},
		"round_results":    RoundResultsPayload{},
		"judging_failed":   JudgingFailedPayload{},
		"game_end":         GameEndPayload{},
		"error":            ErrorPayload{},
	}
	for want, p := range payloads {
		if got := string(p.EventType()); got != want {
			t.Errorf("%T.EventType() = %q, want %q", p, got, want)
		}
	}
}

func TestEncodeProducesTheEnvelope(t *testing.T) {
	msg, err := Encode(ErrorPayload{Code: CodeUnknownEvent, Message: "nope"})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if msg.Type != TypeError {
		t.Errorf("Type = %q, want error", msg.Type)
	}

	var env Envelope
	if err := json.Unmarshal(msg.Bytes(), &env); err != nil {
		t.Fatalf("decoding envelope: %v", err)
	}
	if env.Type != TypeError {
		t.Errorf("envelope type = %q, want error", env.Type)
	}
	var payload ErrorPayload
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	if payload.Code != CodeUnknownEvent || payload.Message != "nope" {
		t.Errorf("payload = %+v", payload)
	}
}

func liveState(withDraft bool) *domain.LiveGameState {
	host, guest := uuid.New(), uuid.New()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	live := &domain.LiveGameState{
		Lobby: domain.LobbyState{
			GameID: uuid.New(), RoomCode: "004213", HostID: host, Status: domain.GameStatusInProgress,
			Settings: domain.GameSettings{
				Rounds: 5, TimePerRoundSeconds: 90, Difficulty: domain.DifficultyMedium,
				Category: domain.CategoryCoding, AIModel: domain.AIModelClaudeSonnet5, MaxPlayers: 4,
			},
		},
		Players: []domain.LivePlayer{
			{UserID: host, Username: "host", JoinedAt: now},
			{UserID: guest, Username: "guest", JoinedAt: now.Add(time.Second)},
		},
		Ready:          []uuid.UUID{guest},
		Round:          &domain.RoundState{Number: 2, ChallengeID: uuid.New(), StartedAt: now, Deadline: now.Add(90 * time.Second)},
		RoundRemaining: 45 * time.Second,
		Points:         map[uuid.UUID]int64{guest: 7},
	}
	if withDraft {
		live.Draft = &domain.Draft{Content: "my private prompt", RoundNumber: 2, UpdatedAt: now}
	}
	return live
}

func TestGameStateFrom(t *testing.T) {
	live := liveState(true)

	own := GameStateFrom(live, true)
	if own.Game.RoomCode != "004213" || own.Game.Status != "in_progress" || own.Game.Settings.TimePerRound != 90 {
		t.Errorf("game = %+v", own.Game)
	}
	if len(own.Players) != 2 || !own.Players[0].IsHost || own.Players[1].IsHost {
		t.Errorf("players = %+v", own.Players)
	}
	if own.Players[1].IsReady != true || own.Players[1].Points != 7 {
		t.Errorf("ready/points not mapped: %+v", own.Players[1])
	}
	if own.Round == nil || own.Round.RemainingMs != 45000 {
		t.Errorf("round = %+v, want remaining time from the server deadline", own.Round)
	}
	if own.YourDraft == nil || own.YourDraft.Content != "my private prompt" {
		t.Errorf("your_draft = %+v, want the caller's own draft", own.YourDraft)
	}

	broadcast := GameStateFrom(live, false)
	if broadcast.YourDraft != nil {
		t.Error("a broadcast game_state carries a draft")
	}
	raw, err := json.Marshal(broadcast)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "my private prompt") {
		t.Errorf("broadcast payload leaks draft content: %s", raw)
	}
	if strings.Contains(string(raw), "your_draft") {
		t.Errorf("your_draft should be omitted from a broadcast: %s", raw)
	}
}

func TestPlayerEventsFromGame(t *testing.T) {
	host, guest := uuid.New(), uuid.New()
	now := time.Now().UTC()
	game := &domain.Game{
		ID: uuid.New(), RoomCode: "123456", HostID: host, Status: domain.GameStatusWaiting, PlayerCount: 2,
		Players: []domain.GamePlayer{
			{UserID: host, Username: "host", JoinedAt: now},
			{UserID: guest, Username: "guest", JoinedAt: now.Add(time.Second)},
		},
	}

	joined := PlayerJoinedFrom(game, guest)
	if joined.Player.UserID != guest.String() || joined.Player.Username != "guest" || joined.Player.IsHost || joined.PlayerCount != 2 {
		t.Errorf("player_joined = %+v", joined)
	}

	left := PlayerLeftFrom(game, host, "host", true)
	if left.UserID != host.String() || left.NewHostID != host.String() {
		t.Errorf("player_left = %+v", left)
	}
	if noChange := PlayerLeftFrom(game, guest, "guest", false); noChange.NewHostID != "" {
		t.Errorf("new_host_id = %q, want empty when hosting did not move", noChange.NewHostID)
	}

	state := GameStateFromGame(game, time.Now())
	if state.Game.RoomCode != "123456" || len(state.Players) != 2 || state.Round != nil || state.YourDraft != nil {
		t.Errorf("game_state from game = %+v", state)
	}
}

func TestGameStateCarriesEngineStateOnTheWire(t *testing.T) {
	now := time.UnixMilli(1759100000123).UTC()
	roundID := uuid.New()
	game := &domain.Game{ID: uuid.New(), RoomCode: "123456", Progress: domain.GameProgress{
		Phase:          domain.GamePhaseRoundActive,
		CurrentRound:   2,
		TotalRounds:    5,
		RoundID:        roundID,
		RoundStartedAt: time.UnixMilli(1759100000000).UTC(),
		RoundDeadline:  time.UnixMilli(1759100060000).UTC(),
	}}

	decode := func(p GameStatePayload) map[string]any {
		t.Helper()
		msg, err := Encode(p)
		if err != nil {
			t.Fatal(err)
		}
		var env struct {
			Type    Type           `json:"type"`
			Payload map[string]any `json:"payload"`
		}
		if err := json.Unmarshal(msg.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if env.Type != TypeGameState {
			t.Fatalf("type = %q", env.Type)
		}
		return env.Payload
	}

	fromGame := decode(GameStateFromGame(game, now))
	fromLive := decode(GameStateFrom(&domain.LiveGameState{
		Lobby: domain.LobbyState{GameID: game.ID}, Progress: game.Progress, ServerTime: now,
	}, false))
	for name, got := range map[string]map[string]any{"from game": fromGame, "from live state": fromLive} {
		want := map[string]any{
			"state":            "RoundActive",
			"current_round":    float64(2),
			"total_rounds":     float64(5),
			"round_id":         roundID.String(),
			"round_started_at": float64(1759100000000),
			"round_deadline":   float64(1759100060000),
			"server_time":      float64(1759100000123),
		}
		for key, value := range want {
			if got[key] != value {
				t.Errorf("%s: %s = %v, want %v", name, key, got[key], value)
			}
		}
	}

	lobby := decode(GameStateFromGame(&domain.Game{Progress: domain.GameProgress{Phase: domain.GamePhaseLobby, TotalRounds: 5}}, now))
	for _, key := range []string{"round_id", "round_started_at", "round_deadline"} {
		if _, ok := lobby[key]; ok {
			t.Errorf("lobby game_state has %s, want it omitted before the first round", key)
		}
	}
}

func TestRoundStartFromUsesServerTiming(t *testing.T) {
	now := time.Now().UTC()
	round := domain.RoundState{Number: 1, ChallengeID: uuid.New(), StartedAt: now, Deadline: now.Add(time.Minute)}
	challenge := &domain.Challenge{
		ID: round.ChallengeID, Title: "Fizz", Description: "d",
		Category: domain.CategoryCoding, Difficulty: domain.DifficultyEasy,
		Constraints: domain.ChallengeConstraints{TimeLimitSeconds: 60, MaxPromptChars: 200},
	}

	payload := RoundStartFrom(round, 60000, challenge)
	if payload.Round.Number != 1 || payload.Round.RemainingMs != 60000 || !payload.Round.Deadline.Equal(round.Deadline) {
		t.Errorf("round = %+v", payload.Round)
	}
	if payload.Challenge.Title != "Fizz" || payload.Challenge.Category != "coding" {
		t.Errorf("challenge = %+v", payload.Challenge)
	}
	if payload.Challenge.Constraints == nil {
		t.Error("constraints missing from round_start")
	}
}
