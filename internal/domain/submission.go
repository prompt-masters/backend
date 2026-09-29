package domain

import (
	"time"

	"github.com/google/uuid"
)

// Submission is a player's final prompt for one round. A player submits at
// most once per round.
type Submission struct {
	RoundID     uuid.UUID
	GameID      uuid.UUID
	RoundNumber int
	UserID      uuid.UUID
	Prompt      string
	SubmittedAt time.Time
}
