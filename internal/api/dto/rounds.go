package dto

import (
	"github.com/prompt-masters/backend/internal/domain"
)

type SubmitRequest struct {
	TestID string `json:"test_id"`
}

// SubmissionResponse is a final submission. Timestamps are UTC Unix ms.
type SubmissionResponse struct {
	ID           string                  `json:"id"`
	RoundID      string                  `json:"round_id"`
	PlayerID     string                  `json:"player_id"`
	TestID       string                  `json:"test_id"`
	Prompt       string                  `json:"prompt"`
	Output       string                  `json:"output"`
	PromptLength int                     `json:"prompt_length"`
	TestCount    int                     `json:"test_count"`
	Source       domain.SubmissionSource `json:"source"`
	SubmittedAt  int64                   `json:"submitted_at"`
	LockedAt     int64                   `json:"locked_at"`
}

type ScoresResponse struct {
	Accuracy   float64 `json:"accuracy"`
	Simplicity float64 `json:"simplicity"`
	Creativity float64 `json:"creativity"`
	Adherence  float64 `json:"adherence"`
	Efficiency float64 `json:"efficiency"`
	Total      float64 `json:"total"`
}

// ScoreResponse is a submission's published score, on a 0-10 scale.
type ScoreResponse struct {
	SubmissionID string         `json:"submission_id"`
	RoundID      string         `json:"round_id"`
	PlayerID     string         `json:"player_id"`
	DisplayName  string         `json:"display_name"`
	Rank         int            `json:"rank"`
	Scores       ScoresResponse `json:"scores"`
	Rationale    string         `json:"rationale"`
	PromptLength int            `json:"prompt_length"`
	TestCount    int            `json:"test_count"`
}

func SubmissionFromDomain(s *domain.Submission) SubmissionResponse {
	return SubmissionResponse{
		ID:           s.ID.String(),
		RoundID:      s.RoundID.String(),
		PlayerID:     s.PlayerID.String(),
		TestID:       s.TestID.String(),
		Prompt:       s.Prompt,
		Output:       s.Output,
		PromptLength: s.PromptLength,
		TestCount:    s.TestCount,
		Source:       s.Source,
		SubmittedAt:  s.SubmittedAt.UnixMilli(),
		LockedAt:     s.LockedAt.UnixMilli(),
	}
}

func ScoreFromDomain(r *domain.RoundResult) ScoreResponse {
	c := r.Components
	return ScoreResponse{
		SubmissionID: r.SubmissionID.String(),
		RoundID:      r.RoundID.String(),
		PlayerID:     r.PlayerID.String(),
		DisplayName:  r.DisplayName,
		Rank:         r.Rank,
		Scores: ScoresResponse{
			Accuracy: c.Accuracy.Float(), Simplicity: c.Simplicity.Float(), Creativity: c.Creativity.Float(),
			Adherence: c.Adherence.Float(), Efficiency: c.Efficiency.Float(), Total: r.Total.Float(),
		},
		Rationale:    r.Rationale,
		PromptLength: r.PromptLength,
		TestCount:    r.TestCount,
	}
}
