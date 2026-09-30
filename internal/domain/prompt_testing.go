package domain

import (
	"time"

	"github.com/google/uuid"
)

// Round is one played round of a game, kept after the game moves on.
type Round struct {
	ID        uuid.UUID
	GameID    uuid.UUID
	Number    int
	StartedAt time.Time
	Deadline  time.Time
}

// PromptTestStatus is where a prompt test stands: pending, then streaming,
// then one of the terminal statuses.
type PromptTestStatus string

const (
	PromptTestPending   PromptTestStatus = "pending"
	PromptTestStreaming PromptTestStatus = "streaming"
	PromptTestCompleted PromptTestStatus = "completed"
	PromptTestFailed    PromptTestStatus = "failed"
	PromptTestCanceled  PromptTestStatus = "canceled"
)

// Terminal reports whether the test has finished.
func (s PromptTestStatus) Terminal() bool {
	return s == PromptTestCompleted || s == PromptTestFailed || s == PromptTestCanceled
}

// PromptTest is one run of a player's draft prompt during a round. Every
// attempt is kept, failed and canceled ones included, for efficiency scoring
// and replay.
type PromptTest struct {
	ID       uuid.UUID
	GameID   uuid.UUID
	RoundID  uuid.UUID
	PlayerID uuid.UUID
	// TestNumber counts the player's tests in the round from 1, with no gaps.
	TestNumber int
	Prompt     string
	// Output is nil until the test ends; a failed test keeps any partial
	// output.
	Output *string
	Status PromptTestStatus
	// ErrorCode is empty unless the test failed or was canceled.
	ErrorCode    string
	Provider     string
	Model        string
	InputTokens  *int
	OutputTokens *int
	CostUSD      *float64
	LatencyMs    *int64
	CreatedAt    time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
}

// PromptTestResult is how a test ended.
type PromptTestResult struct {
	Status       PromptTestStatus
	Output       *string
	ErrorCode    string
	Provider     string
	Model        string
	InputTokens  *int
	OutputTokens *int
	CostUSD      *float64
	LatencyMs    *int64
	CompletedAt  time.Time
}
