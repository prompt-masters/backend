package domain

import (
	"time"

	"github.com/google/uuid"
)

type SubmissionSource string

const (
	// SubmissionManual is a submission the player chose.
	SubmissionManual SubmissionSource = "manual"
	// SubmissionAuto is the player's last completed test, submitted for them
	// when the round's time ran out.
	SubmissionAuto SubmissionSource = "auto"
)

// Submission is a player's final entry for a round: a snapshot of one of
// their completed prompt tests. It is locked when written and never changes.
type Submission struct {
	ID       uuid.UUID
	GameID   uuid.UUID
	RoundID  uuid.UUID
	PlayerID uuid.UUID
	TestID   uuid.UUID
	Prompt   string
	Output   string
	// PromptLength is PromptLength(Prompt).
	PromptLength int
	// TestCount is how many prompt tests the player had run in the round
	// when the submission was made, whatever their status.
	TestCount   int
	Source      SubmissionSource
	SubmittedAt time.Time
	LockedAt    time.Time
}

type JudgingRunStatus string

const (
	JudgingRunning   JudgingRunStatus = "running"
	JudgingCompleted JudgingRunStatus = "completed"
	JudgingFailed    JudgingRunStatus = "failed"
)

// JudgingRun tracks judging a round. Its Attempt identifies the worker that
// claimed it.
type JudgingRun struct {
	RoundID   uuid.UUID
	GameID    uuid.UUID
	Status    JudgingRunStatus
	Attempt   int
	StartedAt time.Time
	ErrorCode string
}

// JudgeCall describes the judge call that scored a round.
type JudgeCall struct {
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      *float64
}

// Score is one player's judged result for a round. A player without a
// submission has a zero score with NoSubmission set and no SubmissionID.
type Score struct {
	GameID       uuid.UUID
	RoundID      uuid.UUID
	PlayerID     uuid.UUID
	SubmissionID uuid.UUID
	NoSubmission bool
	// Label is the anonymous name the judge knew the submission by.
	Label      string
	Components ScoreComponents
	Total      Points
	Rank       int
	Rationale  string
	Provider   string
	Model      string
	CreatedAt  time.Time
}

// RoundResult is one player's published result: their submission and score.
type RoundResult struct {
	Score
	DisplayName  string
	Prompt       string
	Output       string
	PromptLength int
	TestCount    int
	Source       SubmissionSource
}

// RoundResults are a round's results, best rank first.
type RoundResults struct {
	RoundID uuid.UUID
	Number  int
	Results []RoundResult
}
