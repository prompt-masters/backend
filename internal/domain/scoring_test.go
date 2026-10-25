package domain

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPointsFrom(t *testing.T) {
	for in, want := range map[float64]Points{0: 0, 7.6: 760, 7.605: 761, 7.604: 760, 10: 1000, 12: 1000, -1: 0} {
		if got := PointsFrom(in); got != want {
			t.Errorf("PointsFrom(%v) = %d, want %d", in, got, want)
		}
	}
}

func TestEfficiency(t *testing.T) {
	tests := []struct {
		name         string
		length, runs int
		want         Points
	}{
		{"one test, short prompt", 50, 1, 1000},
		{"one test, ideal length", EfficiencyIdealLength, 1, 1000},
		{"three tests, short prompt", 50, 3, 900},  // (8 + 10) / 2
		{"one test, halfway to max", 1050, 1, 750}, // (10 + 5) / 2
		{"one test, at max length", EfficiencyMaxLength, 1, 500},
		{"one test, past max length", 5000, 1, 500},
		{"eleven tests, short prompt", 50, 11, 500}, // (0 + 10) / 2
		{"twenty tests, too long", 3000, 20, 0},
		{"zero tests counts as one", 50, 0, 1000},
	}
	for _, tt := range tests {
		if got := Efficiency(tt.length, tt.runs); got != tt.want {
			t.Errorf("%s: Efficiency(%d, %d) = %d, want %d", tt.name, tt.length, tt.runs, got, tt.want)
		}
	}
	// Fewer tests and shorter prompts never score lower.
	for runs := 1; runs < 15; runs++ {
		if Efficiency(100, runs) < Efficiency(100, runs+1) {
			t.Fatalf("efficiency rises with more tests at %d", runs)
		}
	}
	for length := 0; length < 2500; length += 50 {
		if Efficiency(length, 1) < Efficiency(length+50, 1) {
			t.Fatalf("efficiency rises with a longer prompt at %d", length)
		}
	}
}

func TestTotal(t *testing.T) {
	c := ScoreComponents{Accuracy: 800, Simplicity: 700, Creativity: 900, Adherence: 800, Efficiency: 600}
	if got := Total(c, DefaultScoreWeights); got != 760 {
		t.Errorf("equal weights total = %d, want 760 (the mean)", got)
	}
	accuracyOnly := ScoreWeights{Accuracy: 1}
	if got := Total(c, accuracyOnly); got != 800 {
		t.Errorf("accuracy-only total = %d, want 800", got)
	}
	// Weights are normalized, so scaling them changes nothing.
	if Total(c, ScoreWeights{Accuracy: 2, Simplicity: 2, Creativity: 2, Adherence: 2, Efficiency: 2}) != Total(c, DefaultScoreWeights) {
		t.Error("scaled weights changed the total")
	}
	// Rounded to the nearest hundredth: (1+0+0)/3 = 0.333... points.
	if got := Total(ScoreComponents{Accuracy: 100}, ScoreWeights{Accuracy: 1, Simplicity: 1, Creativity: 1}); got != 33 {
		t.Errorf("rounded total = %d, want 33", got)
	}
	if !DefaultScoreWeights.Valid() || (ScoreWeights{}).Valid() || (ScoreWeights{Accuracy: -1, Simplicity: 2}).Valid() {
		t.Error("Valid() misjudges weights")
	}
}

func TestPromptLength(t *testing.T) {
	for in, want := range map[string]int{"hello": 5, "  hello \n": 5, "héllo": 5, "日本語のプロンプト": 9, "👋🏽 hi": 5 /* code points, not graphemes: 👋 + skin tone */, "": 0} {
		if got := PromptLength(in); got != want {
			t.Errorf("PromptLength(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestRank(t *testing.T) {
	t0 := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	entry := func(total, accuracy Points, tests, length int, after time.Duration) RankEntry {
		return RankEntry{PlayerID: uuid.New(), Total: total, Accuracy: accuracy, TestCount: tests, PromptLength: length, SubmittedAt: t0.Add(after)}
	}
	none := RankEntry{PlayerID: uuid.New(), NoSubmission: true}

	tests := []struct {
		name    string
		entries []RankEntry
		want    []int
	}{
		{"by total", []RankEntry{entry(500, 5, 1, 10, 0), entry(900, 5, 1, 10, 0), entry(700, 5, 1, 10, 0)}, []int{3, 1, 2}},
		{"tie on total: higher accuracy", []RankEntry{entry(700, 600, 1, 10, 0), entry(700, 800, 1, 10, 0)}, []int{2, 1}},
		{"then fewer tests", []RankEntry{entry(700, 800, 3, 10, 0), entry(700, 800, 2, 10, 0)}, []int{2, 1}},
		{"then shorter prompt", []RankEntry{entry(700, 800, 2, 90, 0), entry(700, 800, 2, 80, 0)}, []int{2, 1}},
		{"then earlier submission", []RankEntry{entry(700, 800, 2, 80, time.Second), entry(700, 800, 2, 80, 0)}, []int{2, 1}},
		{"full tie shares the rank", []RankEntry{entry(900, 1, 1, 1, 0), entry(700, 800, 2, 80, 0), entry(700, 800, 2, 80, 0), entry(100, 1, 1, 1, 0)}, []int{1, 2, 2, 4}},
		{"no submission last", []RankEntry{none, entry(0, 0, 20, 5000, 0), entry(500, 5, 1, 10, 0)}, []int{3, 2, 1}},
		{"several without submission share last", []RankEntry{none, entry(500, 5, 1, 10, 0), {PlayerID: uuid.New(), NoSubmission: true}}, []int{2, 1, 2}},
		{"nobody submitted", []RankEntry{none, {PlayerID: uuid.New(), NoSubmission: true}}, []int{1, 1}},
		{"empty", nil, []int{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Rank(tt.entries); !slices.Equal(got, tt.want) {
				t.Errorf("Rank() = %v, want %v", got, tt.want)
			}
		})
	}
}
