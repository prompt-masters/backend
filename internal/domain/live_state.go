package domain

import (
	"time"

	"github.com/google/uuid"
)

// MaxDraftBytes caps a stored draft prompt.
const MaxDraftBytes = 8 << 10

// LobbyState is the live snapshot of a game's lobby: who hosts it, its status
// and the settings in force.
type LobbyState struct {
	GameID    uuid.UUID
	RoomCode  string
	HostID    uuid.UUID
	Status    GameStatus
	Settings  GameSettings
	UpdatedAt time.Time
}

type LivePlayer struct {
	UserID   uuid.UUID
	Username string
	JoinedAt time.Time
}

// RoundState describes the round in play. StartedAt and Deadline are always
// set by the server; clients never supply them.
type RoundState struct {
	Number      int
	ChallengeID uuid.UUID
	StartedAt   time.Time
	Deadline    time.Time
}

// Remaining is the time left before the deadline, never negative.
func (r RoundState) Remaining(now time.Time) time.Duration {
	return max(r.Deadline.Sub(now), 0)
}

// Draft is a player's unsubmitted prompt. It is only ever shown to its author.
type Draft struct {
	Content     string
	RoundNumber int
	UpdatedAt   time.Time
}

// LiveGameState is everything a reconnecting player needs to resume a game.
type LiveGameState struct {
	Lobby   LobbyState
	Players []LivePlayer
	Ready   []uuid.UUID
	Round   *RoundState
	// RoundRemaining is computed from the server deadline at read time.
	RoundRemaining time.Duration
	// Draft is the requesting player's own draft, if any.
	Draft  *Draft
	Points map[uuid.UUID]int64
	// Rebuilt reports that Redis held no state and it was reconstructed from
	// PostgreSQL.
	Rebuilt bool
}
