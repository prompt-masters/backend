package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestChallengeConstraintsDeserializeFromJSON(t *testing.T) {
	raw := `{
		"time_limit_seconds": 180,
		"max_prompt_chars": 400,
		"forbidden_words": ["poem", "rhyme"],
		"required_elements": ["a twist ending"],
		"output_format": "plain_text"
	}`

	var got ChallengeConstraints
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	want := ChallengeConstraints{
		TimeLimitSeconds: 180,
		MaxPromptChars:   400,
		ForbiddenWords:   []string{"poem", "rhyme"},
		RequiredElements: []string{"a twist ending"},
		OutputFormat:     "plain_text",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("constraints = %+v, want %+v", got, want)
	}
}

func TestJudgeCriteriaDeserializeFromJSON(t *testing.T) {
	raw := `{
		"passing_score": 60,
		"criteria": [
			{"name": "accuracy", "description": "Output is correct.", "weight": 0.7, "max_score": 10},
			{"name": "brevity", "description": "Prompt is concise.", "weight": 0.3, "max_score": 5}
		]
	}`

	var got JudgeCriteria
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	want := JudgeCriteria{
		PassingScore: 60,
		Criteria: []JudgeCriterion{
			{Name: "accuracy", Description: "Output is correct.", Weight: 0.7, MaxScore: 10},
			{Name: "brevity", Description: "Prompt is concise.", Weight: 0.3, MaxScore: 5},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("judge criteria = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestJudgeCriteriaValidate(t *testing.T) {
	valid := func() JudgeCriteria {
		return JudgeCriteria{
			PassingScore: 60,
			Criteria: []JudgeCriterion{
				{Name: "accuracy", Description: "d", Weight: 0.5, MaxScore: 10},
				{Name: "clarity", Description: "d", Weight: 0.5, MaxScore: 10},
			},
		}
	}

	tests := []struct {
		name    string
		mutate  func(*JudgeCriteria)
		wantErr string
	}{
		{name: "valid rubric", mutate: func(*JudgeCriteria) {}},
		{name: "passing score above 100", mutate: func(j *JudgeCriteria) { j.PassingScore = 101 }, wantErr: "passing_score"},
		{name: "no criteria", mutate: func(j *JudgeCriteria) { j.Criteria = nil }, wantErr: "at least one criterion"},
		{name: "blank name", mutate: func(j *JudgeCriteria) { j.Criteria[0].Name = " " }, wantErr: "name is required"},
		{name: "duplicate name", mutate: func(j *JudgeCriteria) { j.Criteria[1].Name = "accuracy" }, wantErr: "duplicated"},
		{name: "blank description", mutate: func(j *JudgeCriteria) { j.Criteria[0].Description = "" }, wantErr: "description is required"},
		{name: "zero weight", mutate: func(j *JudgeCriteria) { j.Criteria[0].Weight = 0 }, wantErr: "weight must be positive"},
		{name: "zero max score", mutate: func(j *JudgeCriteria) { j.Criteria[0].MaxScore = 0 }, wantErr: "max_score must be positive"},
		{name: "weights do not sum to 1", mutate: func(j *JudgeCriteria) { j.Criteria[0].Weight = 0.4 }, wantErr: "sum to 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rubric := valid()
			tt.mutate(&rubric)

			err := rubric.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestCategoryAndDifficultyValid(t *testing.T) {
	for _, c := range Categories() {
		if !c.Valid() {
			t.Errorf("Category(%q).Valid() = false, want true", c)
		}
	}
	for _, d := range Difficulties() {
		if !d.Valid() {
			t.Errorf("Difficulty(%q).Valid() = false, want true", d)
		}
	}
	if Category("cooking").Valid() {
		t.Error(`Category("cooking").Valid() = true, want false`)
	}
	if Difficulty("EASY").Valid() {
		t.Error(`Difficulty("EASY").Valid() = true, want false`)
	}
}
