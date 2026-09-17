package domain

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Difficulty string

const (
	DifficultyEasy   Difficulty = "easy"
	DifficultyMedium Difficulty = "medium"
	DifficultyHard   Difficulty = "hard"
)

// Difficulties returns every difficulty level, easiest first.
func Difficulties() []Difficulty {
	return []Difficulty{DifficultyEasy, DifficultyMedium, DifficultyHard}
}

func (d Difficulty) Valid() bool {
	switch d {
	case DifficultyEasy, DifficultyMedium, DifficultyHard:
		return true
	}
	return false
}

type Category string

const (
	CategoryCreativeWriting Category = "creative_writing"
	CategoryCoding          Category = "coding"
	CategoryDataExtraction  Category = "data_extraction"
	CategorySummarization   Category = "summarization"
	CategoryReasoning       Category = "reasoning"
)

// Categories returns every challenge category. The database enforces the
// same set through a CHECK constraint on challenges.category.
func Categories() []Category {
	return []Category{
		CategoryCreativeWriting,
		CategoryCoding,
		CategoryDataExtraction,
		CategorySummarization,
		CategoryReasoning,
	}
}

func (c Category) Valid() bool {
	switch c {
	case CategoryCreativeWriting, CategoryCoding, CategoryDataExtraction, CategorySummarization, CategoryReasoning:
		return true
	}
	return false
}

type Challenge struct {
	ID            uuid.UUID
	Slug          string
	Title         string
	Description   string
	Category      Category
	Difficulty    Difficulty
	Constraints   ChallengeConstraints
	JudgeCriteria JudgeCriteria
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ChallengeConstraints are the rules a player's prompt must respect. It is
// stored as JSONB in challenges.constraints.
type ChallengeConstraints struct {
	// TimeLimitSeconds is how long the player has to submit a prompt.
	TimeLimitSeconds int `json:"time_limit_seconds"`
	// MaxPromptChars caps the length of the submitted prompt; 0 means no cap.
	MaxPromptChars int `json:"max_prompt_chars"`
	// ForbiddenWords may not appear in the submitted prompt.
	ForbiddenWords []string `json:"forbidden_words"`
	// RequiredElements must be present in the model output the prompt produces.
	RequiredElements []string `json:"required_elements"`
	// OutputFormat describes the expected shape of the output, e.g. "json".
	OutputFormat string `json:"output_format,omitempty"`
}

// JudgeCriteria is the rubric the AI Judge scores a submission against. It is
// stored as JSONB in challenges.judge_criteria.
type JudgeCriteria struct {
	// PassingScore is the weighted score, on a 0–100 scale, needed to pass.
	PassingScore int              `json:"passing_score"`
	Criteria     []JudgeCriterion `json:"criteria"`
}

type JudgeCriterion struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Weight is this criterion's share of the final score; the weights of all
	// criteria in a rubric sum to 1.
	Weight float64 `json:"weight"`
	// MaxScore is the highest raw score the judge may award this criterion.
	MaxScore int `json:"max_score"`
}

const weightSumTolerance = 1e-6

// Validate reports whether the rubric is complete enough for the AI Judge to
// score against.
func (j JudgeCriteria) Validate() error {
	var errs []error
	if j.PassingScore < 0 || j.PassingScore > 100 {
		errs = append(errs, fmt.Errorf("passing_score must be between 0 and 100, got %d", j.PassingScore))
	}
	if len(j.Criteria) == 0 {
		errs = append(errs, errors.New("at least one criterion is required"))
	}

	var weights float64
	names := make(map[string]bool, len(j.Criteria))
	for i, c := range j.Criteria {
		if strings.TrimSpace(c.Name) == "" {
			errs = append(errs, fmt.Errorf("criteria[%d].name is required", i))
		} else if names[c.Name] {
			errs = append(errs, fmt.Errorf("criteria[%d].name %q is duplicated", i, c.Name))
		}
		names[c.Name] = true
		if strings.TrimSpace(c.Description) == "" {
			errs = append(errs, fmt.Errorf("criteria[%d].description is required", i))
		}
		if c.Weight <= 0 {
			errs = append(errs, fmt.Errorf("criteria[%d].weight must be positive", i))
		}
		if c.MaxScore <= 0 {
			errs = append(errs, fmt.Errorf("criteria[%d].max_score must be positive", i))
		}
		weights += c.Weight
	}
	if len(j.Criteria) > 0 && math.Abs(weights-1) > weightSumTolerance {
		errs = append(errs, fmt.Errorf("criteria weights must sum to 1, got %g", weights))
	}

	return errors.Join(errs...)
}

// ChallengeFilter narrows a challenge listing. Nil fields do not filter.
type ChallengeFilter struct {
	Category   *Category
	Difficulty *Difficulty
}
