package domain

import (
	"regexp"
	"time"

	"github.com/google/uuid"
)

type GameStatus string

const (
	GameStatusWaiting    GameStatus = "waiting"
	GameStatusInProgress GameStatus = "in_progress"
	GameStatusFinished   GameStatus = "finished"
	GameStatusCancelled  GameStatus = "cancelled"
)

func GameStatuses() []GameStatus {
	return []GameStatus{GameStatusWaiting, GameStatusInProgress, GameStatusFinished, GameStatusCancelled}
}

func (s GameStatus) Valid() bool {
	switch s {
	case GameStatusWaiting, GameStatusInProgress, GameStatusFinished, GameStatusCancelled:
		return true
	}
	return false
}

// Active reports whether the game still holds its room code.
func (s GameStatus) Active() bool {
	return s == GameStatusWaiting || s == GameStatusInProgress
}

// Cancellable reports whether a game in this status may be cancelled.
func (s GameStatus) Cancellable() bool {
	return s.Active()
}

// AIModel identifies the model that plays player prompts. The database
// enforces the same set through a CHECK constraint on game_settings.ai_model.
type AIModel string

const (
	AIModelClaudeOpus5    AIModel = "claude-opus-5"
	AIModelClaudeSonnet5  AIModel = "claude-sonnet-5"
	AIModelClaudeHaiku4_5 AIModel = "claude-haiku-4-5"
)

func AIModels() []AIModel {
	return []AIModel{AIModelClaudeOpus5, AIModelClaudeSonnet5, AIModelClaudeHaiku4_5}
}

func (m AIModel) Valid() bool {
	switch m {
	case AIModelClaudeOpus5, AIModelClaudeSonnet5, AIModelClaudeHaiku4_5:
		return true
	}
	return false
}

const (
	RoomCodeLength = 6
	// MinPlayers is both the smallest allowed max_players and the number of
	// players needed to start a game.
	MinPlayers = 2
	MaxPlayers = 6
)

var roomCodePattern = regexp.MustCompile(`^[0-9]{6}$`)

// IsRoomCode reports whether s has the shape of a room code: exactly six
// ASCII digits, leading zeros included.
func IsRoomCode(s string) bool {
	return roomCodePattern.MatchString(s)
}

var (
	AllowedRounds              = []int{3, 5, 7}
	AllowedTimePerRoundSeconds = []int{60, 90, 120}
)

type GameSettings struct {
	Rounds              int
	TimePerRoundSeconds int
	Difficulty          Difficulty
	Category            Category
	AIModel             AIModel
	MaxPlayers          int
}

type Game struct {
	ID          uuid.UUID
	RoomCode    string
	HostID      uuid.UUID
	Status      GameStatus
	Settings    GameSettings
	PlayerCount int
	// Players is only loaded for single-game lookups, ordered by join time.
	Players   []GamePlayer
	CreatedAt time.Time
	UpdatedAt time.Time
	StartedAt *time.Time
}

func (g *Game) IsHost(userID uuid.UUID) bool {
	return g.HostID == userID
}

func (g *Game) HasPlayer(userID uuid.UUID) bool {
	for _, p := range g.Players {
		if p.UserID == userID {
			return true
		}
	}
	return false
}

type GamePlayer struct {
	UserID   uuid.UUID
	Username string
	JoinedAt time.Time
}
