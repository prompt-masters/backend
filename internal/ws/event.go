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
	TypeOutputError     Type = "output_error"
	TypePlayerSubmitted Type = "player_submitted"
	TypeRoundEnd        Type = "round_end"
	TypeRoundResults    Type = "round_results"
	TypeJudgingFailed   Type = "judging_failed"
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

	// The game engine's state, always read from the database. Timestamps are
	// UTC Unix milliseconds. A client renders the countdown as
	// round_deadline - (client_now + server_time - client_receive_time); a
	// reconnect gets the stored deadline, so a refresh never resets it.
	State        string `json:"state"`
	CurrentRound int    `json:"current_round"`
	TotalRounds  int    `json:"total_rounds"`
	// RoundID, RoundStartedAt and RoundDeadline describe the current or last
	// round and are omitted before the first one.
	RoundID        string `json:"round_id,omitempty"`
	RoundStartedAt int64  `json:"round_started_at,omitempty"`
	RoundDeadline  int64  `json:"round_deadline,omitempty"`
	ServerTime     int64  `json:"server_time"`
	// ChallengeID is the current round's challenge; its details are at
	// GET /api/v1/challenges/{id}.
	ChallengeID string `json:"challenge_id,omitempty"`
	// RoundResults are the last round's published results, sent while the
	// game shows Results or is over, so a reconnect sees what the room saw.
	RoundResults *RoundResultsPayload `json:"round_results,omitempty"`
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

// OutputChunkPayload streams one piece of a player's own prompt test output,
// so it is only ever sent to that player. Seq starts at 1 and increases by
// one per chunk of the test, for ordering and de-duplication.
type OutputChunkPayload struct {
	TestID     string `json:"test_id"`
	TestNumber int    `json:"test_number"`
	Seq        int    `json:"seq"`
	Delta      string `json:"delta"`
}

func (OutputChunkPayload) EventType() Type { return TypeOutputChunk }

type UsageInfo struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// OutputCompletePayload ends a successful test. Output is the stored final
// output, not a reassembly of the chunks.
type OutputCompletePayload struct {
	TestID     string     `json:"test_id"`
	TestNumber int        `json:"test_number"`
	Status     string     `json:"status"`
	Output     string     `json:"output"`
	Usage      *UsageInfo `json:"usage"`
}

func (OutputCompletePayload) EventType() Type { return TypeOutputComplete }

// OutputErrorPayload ends a failed or canceled test. Message is safe to show.
type OutputErrorPayload struct {
	TestID     string `json:"test_id"`
	TestNumber int    `json:"test_number"`
	Status     string `json:"status"`
	ErrorCode  string `json:"error_code"`
	Message    string `json:"message"`
}

func (OutputErrorPayload) EventType() Type { return TypeOutputError }

// PlayerSubmittedPayload announces a submission by count only: no prompt,
// output or score of another player is ever sent before results.
type PlayerSubmittedPayload struct {
	PlayerID       string `json:"player_id"`
	RoundID        string `json:"round_id"`
	SubmittedCount int    `json:"submitted_count"`
	TotalPlayers   int    `json:"total_players"`
}

func (PlayerSubmittedPayload) EventType() Type { return TypePlayerSubmitted }

type RoundEndPayload struct {
	RoundNumber int    `json:"round_number"`
	RoundID     string `json:"round_id,omitempty"`
	// Reason is "all_submitted" or "time_expired".
	Reason string `json:"reason"`
}

func (RoundEndPayload) EventType() Type { return TypeRoundEnd }

// ScoresInfo are a submission's scores on a 0-10 scale, to two decimals.
type ScoresInfo struct {
	Accuracy   float64 `json:"accuracy"`
	Simplicity float64 `json:"simplicity"`
	Creativity float64 `json:"creativity"`
	Adherence  float64 `json:"adherence"`
	Efficiency float64 `json:"efficiency"`
	Total      float64 `json:"total"`
}

// RoundResultEntry is one player's result. It is the first moment another
// player's prompt and output are sent.
type RoundResultEntry struct {
	Rank         int        `json:"rank"`
	PlayerID     string     `json:"player_id"`
	DisplayName  string     `json:"display_name"`
	SubmissionID string     `json:"submission_id,omitempty"`
	Prompt       string     `json:"prompt"`
	Output       string     `json:"output"`
	Scores       ScoresInfo `json:"scores"`
	PromptLength int        `json:"prompt_length"`
	TestCount    int        `json:"test_count"`
	Source       string     `json:"source,omitempty"`
	Rationale    string     `json:"rationale,omitempty"`
	// NoSubmission marks a player who submitted nothing: zero scores, last.
	NoSubmission bool `json:"no_submission"`
}

// RoundResultsPayload is broadcast once a round's scores are committed, best
// rank first.
type RoundResultsPayload struct {
	RoundID string             `json:"round_id"`
	Round   int                `json:"round"`
	Results []RoundResultEntry `json:"results"`
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

// JudgingFailedPayload reports that judging a round failed and the game is
// waiting in Judging; retryable says a retry may succeed.
type JudgingFailedPayload struct {
	RoundID   string `json:"round_id"`
	Retryable bool   `json:"retryable"`
}

func (JudgingFailedPayload) EventType() Type { return TypeJudgingFailed }

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

// ClientSubmitPrompt makes one of the player's completed prompt tests their
// final submission for the round.
type ClientSubmitPrompt struct {
	RoundID string `json:"round_id"`
	TestID  string `json:"test_id"`
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
