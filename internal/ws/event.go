// Package ws carries the real-time game event contract and the WebSocket hub
// that delivers it. It transports events only: game rules live in the
// services that publish them.
package ws

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Type identifies an event. The values are the contract shared with clients.
type Type string

// Server to client.
const (
	TypeGameState       Type = "game_state"
	TypePlayerJoined    Type = "player_joined"
	TypePlayerLeft      Type = "player_left"
	TypePlayerReady     Type = "player_ready"
	TypePlayerTyping    Type = "player_typing"
	TypeRoundStart      Type = "round_start"
	TypeTimerTick       Type = "timer_tick"
	TypeOutputChunk     Type = "output_chunk"
	TypeOutputComplete  Type = "output_complete"
	TypePlayerSubmitted Type = "player_submitted"
	TypeRoundEnd        Type = "round_end"
	TypeRoundResults    Type = "round_results"
	TypeGameEnd         Type = "game_end"
	TypeError           Type = "error"
)

// Client to server.
const (
	TypeClientPlayerReady  Type = "player_ready"
	TypeClientPromptTest   Type = "prompt_test"
	TypeClientPromptTyping Type = "prompt_typing"
	TypeClientSubmitPrompt Type = "submit_prompt"
	TypeClientHeartbeat    Type = "heartbeat"
)

// Error codes carried by ErrorPayload.
const (
	CodeMalformedMessage = "malformed_message"
	CodeUnknownEvent     = "unknown_event"
	CodeUnsupportedEvent = "unsupported_event"
	CodeInvalidPayload   = "invalid_payload"
	CodeNotAllowed       = "not_allowed"
	CodeInternal         = "internal_error"
	CodeStateUnavailable = "state_unavailable"
)

// Envelope is the wire format of every message: {"type": ..., "payload": ...}.
type Envelope struct {
	Type    Type            `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// Payload is one event's body. Each implementation names its event type, so
// the contract is checked at compile time.
type Payload interface {
	EventType() Type
}

// Message is an encoded event, ready to be written to any number of sockets.
type Message struct {
	Type Type
	data []byte
}

// Bytes returns the encoded envelope.
func (m Message) Bytes() []byte { return m.data }

// Encode marshals a payload into its envelope once, so a broadcast to a room
// serializes the event a single time.
func Encode(p Payload) (Message, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return Message{}, fmt.Errorf("encoding %s payload: %w", p.EventType(), err)
	}
	data, err := json.Marshal(Envelope{Type: p.EventType(), Payload: raw})
	if err != nil {
		return Message{}, fmt.Errorf("encoding %s envelope: %w", p.EventType(), err)
	}
	return Message{Type: p.EventType(), data: data}, nil
}

// --- Server to client payloads ---

type PlayerInfo struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	IsHost   bool      `json:"is_host"`
	IsReady  bool      `json:"is_ready"`
	Points   int64     `json:"points"`
	JoinedAt time.Time `json:"joined_at"`
}

type GameInfo struct {
	GameID   string       `json:"game_id"`
	RoomCode string       `json:"room_code"`
	HostID   string       `json:"host_id"`
	Status   string       `json:"status"`
	Settings SettingsInfo `json:"settings"`
}

type SettingsInfo struct {
	Rounds       int    `json:"rounds"`
	TimePerRound int    `json:"time_per_round"`
	Difficulty   string `json:"difficulty"`
	Category     string `json:"category"`
	AIModel      string `json:"ai_model"`
	MaxPlayers   int    `json:"max_players"`
}

// RoundInfo carries server-side timing only: the client never sets it.
type RoundInfo struct {
	Number      int       `json:"number"`
	ChallengeID string    `json:"challenge_id"`
	StartedAt   time.Time `json:"started_at"`
	Deadline    time.Time `json:"deadline"`
	RemainingMs int64     `json:"remaining_ms"`
}

type DraftInfo struct {
	Content     string    `json:"content"`
	RoundNumber int       `json:"round_number"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// GameStatePayload is the full state sent on connect or reconnect. YourDraft
// is only ever set on a targeted send, never on a room broadcast.
type GameStatePayload struct {
	Game      GameInfo     `json:"game"`
	Players   []PlayerInfo `json:"players"`
	Round     *RoundInfo   `json:"round"`
	YourDraft *DraftInfo   `json:"your_draft,omitempty"`
	// Rebuilt reports state rebuilt from the database because live state was
	// missing.
	Rebuilt bool `json:"rebuilt"`
}

func (GameStatePayload) EventType() Type { return TypeGameState }

type PlayerJoinedPayload struct {
	Player      PlayerInfo `json:"player"`
	PlayerCount int        `json:"player_count"`
}

func (PlayerJoinedPayload) EventType() Type { return TypePlayerJoined }

type PlayerLeftPayload struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
	// NewHostID is set when hosting passed to another player.
	NewHostID   string `json:"new_host_id,omitempty"`
	PlayerCount int    `json:"player_count"`
}

func (PlayerLeftPayload) EventType() Type { return TypePlayerLeft }

type PlayerReadyPayload struct {
	UserID string `json:"user_id"`
	Ready  bool   `json:"ready"`
}

func (PlayerReadyPayload) EventType() Type { return TypePlayerReady }

// PlayerTypingPayload is the presence indicator raised by a client's
// prompt_typing message. It never carries prompt text.
type PlayerTypingPayload struct {
	UserID string `json:"user_id"`
}

func (PlayerTypingPayload) EventType() Type { return TypePlayerTyping }

type ChallengeInfo struct {
	ChallengeID string `json:"challenge_id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Difficulty  string `json:"difficulty"`
	Constraints any    `json:"constraints"`
}

type RoundStartPayload struct {
	Round     RoundInfo     `json:"round"`
	Challenge ChallengeInfo `json:"challenge"`
}

func (RoundStartPayload) EventType() Type { return TypeRoundStart }

type TimerTickPayload struct {
	RoundNumber int       `json:"round_number"`
	Deadline    time.Time `json:"deadline"`
	RemainingMs int64     `json:"remaining_ms"`
}

func (TimerTickPayload) EventType() Type { return TypeTimerTick }

// OutputChunkPayload streams one piece of a player's own test output, so it is
// only ever sent to that player.
type OutputChunkPayload struct {
	RoundNumber int    `json:"round_number"`
	TestNumber  int    `json:"test_number"`
	Chunk       string `json:"chunk"`
}

func (OutputChunkPayload) EventType() Type { return TypeOutputChunk }

type OutputCompletePayload struct {
	RoundNumber int    `json:"round_number"`
	TestNumber  int    `json:"test_number"`
	Output      string `json:"output"`
}

func (OutputCompletePayload) EventType() Type { return TypeOutputComplete }

// PlayerSubmittedPayload announces a submission without revealing any prompt,
// which keeps judging blind.
type PlayerSubmittedPayload struct {
	UserID      string    `json:"user_id"`
	RoundNumber int       `json:"round_number"`
	SubmittedAt time.Time `json:"submitted_at"`
}

func (PlayerSubmittedPayload) EventType() Type { return TypePlayerSubmitted }

type RoundEndPayload struct {
	RoundNumber int    `json:"round_number"`
	RoundID     string `json:"round_id,omitempty"`
	// Reason is "all_submitted" or "time_expired".
	Reason string `json:"reason"`
}

func (RoundEndPayload) EventType() Type { return TypeRoundEnd }

type ScoreInfo struct {
	Accuracy            float64 `json:"accuracy"`
	Simplicity          float64 `json:"simplicity"`
	Creativity          float64 `json:"creativity"`
	ConstraintAdherence float64 `json:"constraint_adherence"`
	EfficiencyBonus     float64 `json:"efficiency_bonus"`
	Total               float64 `json:"total"`
}

// RoundResultPayloadEntry is one player's result, released only once judging
// has finished.
type RoundResultPayloadEntry struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	Rank     int       `json:"rank"`
	Prompt   string    `json:"prompt"`
	Output   string    `json:"output"`
	Score    ScoreInfo `json:"score"`
	Feedback string    `json:"feedback,omitempty"`
}

type RoundResultsPayload struct {
	RoundNumber int                       `json:"round_number"`
	Results     []RoundResultPayloadEntry `json:"results"`
}

func (RoundResultsPayload) EventType() Type { return TypeRoundResults }

type FinalRanking struct {
	UserID     string `json:"user_id"`
	Username   string `json:"username"`
	Rank       int    `json:"rank"`
	TotalScore int64  `json:"total_score"`
	EloChange  int    `json:"elo_change"`
}

type GameEndPayload struct {
	Rankings []FinalRanking `json:"rankings"`
	EndedAt  time.Time      `json:"ended_at"`
}

func (GameEndPayload) EventType() Type { return TypeGameEnd }

// ErrorPayload is always sent to a single connection.
type ErrorPayload struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (ErrorPayload) EventType() Type { return TypeError }

// --- Client to server payloads ---

// ClientPlayerReady signals readiness. Ready defaults to true when omitted.
type ClientPlayerReady struct {
	Ready   *bool  `json:"ready"`
	RoundID string `json:"round_id,omitempty"`
}

type ClientPromptTest struct {
	Prompt string `json:"prompt"`
}

type ClientSubmitPrompt struct {
	Prompt string `json:"prompt"`
}

// Inbound is a decoded client message handed to a Router. UserID comes from
// the verified JWT, never from the payload.
type Inbound struct {
	GameID   uuid.UUID
	UserID   uuid.UUID
	Username string
	Type     Type
	Payload  json.RawMessage
	// Reply sends an event to the connection the message arrived on.
	Reply func(Payload)
}

// Router handles client messages. Implementations live in the service layer;
// the hub itself knows no game rules.
type Router interface {
	Route(ctx context.Context, in Inbound)
}
