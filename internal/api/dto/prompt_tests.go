package dto

import "github.com/prompt-masters/backend/internal/domain"

type PromptTestRequest struct {
	Prompt string `json:"prompt"`
}

// PromptTestStartedResponse answers a test request as soon as it is stored.
type PromptTestStartedResponse struct {
	TestID     string                  `json:"test_id"`
	TestNumber int                     `json:"test_number"`
	Status     domain.PromptTestStatus `json:"status"`
}

// PromptTestResponse is one of the caller's own tests. Timestamps are UTC
// Unix milliseconds; fields not yet known are null.
type PromptTestResponse struct {
	TestID       string                  `json:"test_id"`
	TestNumber   int                     `json:"test_number"`
	Prompt       string                  `json:"prompt"`
	Output       *string                 `json:"output"`
	Status       domain.PromptTestStatus `json:"status"`
	ErrorCode    *string                 `json:"error_code"`
	InputTokens  *int                    `json:"input_tokens"`
	OutputTokens *int                    `json:"output_tokens"`
	LatencyMs    *int64                  `json:"latency_ms"`
	CreatedAt    int64                   `json:"created_at"`
	CompletedAt  *int64                  `json:"completed_at"`
}

func PromptTestStartedFromDomain(t *domain.PromptTest) PromptTestStartedResponse {
	return PromptTestStartedResponse{TestID: t.ID.String(), TestNumber: t.TestNumber, Status: t.Status}
}

func PromptTestsFromDomain(tests []domain.PromptTest) []PromptTestResponse {
	out := make([]PromptTestResponse, len(tests))
	for i, t := range tests {
		r := PromptTestResponse{
			TestID:       t.ID.String(),
			TestNumber:   t.TestNumber,
			Prompt:       t.Prompt,
			Output:       t.Output,
			Status:       t.Status,
			InputTokens:  t.InputTokens,
			OutputTokens: t.OutputTokens,
			LatencyMs:    t.LatencyMs,
			CreatedAt:    t.CreatedAt.UnixMilli(),
		}
		if t.ErrorCode != "" {
			code := t.ErrorCode
			r.ErrorCode = &code
		}
		if t.CompletedAt != nil {
			ms := t.CompletedAt.UnixMilli()
			r.CompletedAt = &ms
		}
		out[i] = r
	}
	return out
}
