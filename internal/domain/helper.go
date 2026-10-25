package domain

import (
	"time"

	"github.com/google/uuid"
)

// HelperType is a paid in-round AI helper.
type HelperType string

const (
	// HelperHint nudges toward what the task needs, without solving it.
	HelperHint HelperType = "hint"
	// HelperSuggestion proposes a few concrete edits to the player's draft.
	HelperSuggestion HelperType = "suggestion"
	// HelperReview critiques the player's prompt and, optionally, a test's
	// output against the task.
	HelperReview HelperType = "review"
)

func HelperTypes() []HelperType {
	return []HelperType{HelperHint, HelperSuggestion, HelperReview}
}

func (t HelperType) Valid() bool {
	switch t {
	case HelperHint, HelperSuggestion, HelperReview:
		return true
	}
	return false
}

type HelperStatus string

const (
	// HelperReserved means the points are taken and the helper is running.
	HelperReserved  HelperStatus = "reserved"
	HelperDelivered HelperStatus = "delivered"
	// HelperRefunded means the helper was not delivered and the points were
	// given back.
	HelperRefunded HelperStatus = "refunded"
)

// HelperUsage is one paid helper request. It is created when the points are
// reserved and ends delivered or refunded; it is never deleted.
type HelperUsage struct {
	ID             uuid.UUID
	GameID         uuid.UUID
	RoundID        uuid.UUID
	PlayerID       uuid.UUID
	Type           HelperType
	Cost           int
	Status         HelperStatus
	IdempotencyKey string
	InputPrompt    string
	TestID         uuid.UUID
	Content        string
	ErrorCode      string
	Provider       string
	Model          string
	InputTokens    *int
	OutputTokens   *int
	CostUSD        *float64
	CreatedAt      time.Time
	DeliveredAt    *time.Time
	RefundedAt     *time.Time
}

// HelperDelivery is what a delivered helper produced.
type HelperDelivery struct {
	Content      string
	Provider     string
	Model        string
	InputTokens  int
	OutputTokens int
	CostUSD      *float64
	DeliveredAt  time.Time
}
