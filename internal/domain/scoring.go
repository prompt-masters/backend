package domain

import (
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// Scoring rules. There is no scoring design document yet; these defaults are
// the rules, and every one is a pure function tested on its own.

// ScoreScale is the top of every score component's scale.
const ScoreScale = 10

// Points are hundredths of a point (7.60 is 760), so totals compare exactly.
type Points int

// MaxPoints is ScoreScale in Points.
const MaxPoints = Points(ScoreScale * 100)

// PointsFrom converts a score on the 0..ScoreScale scale, rounding half up
// and clamping to the scale.
func PointsFrom(score float64) Points {
	if math.IsNaN(score) {
		return 0
	}
	p := Points(math.Floor(score*100 + 0.5))
	return min(max(p, 0), MaxPoints)
}

// Float is the score as a number on the 0..ScoreScale scale.
func (p Points) Float() float64 { return float64(p) / 100 }

// ScoreComponents are a submission's scores. Accuracy, simplicity,
// creativity and adherence come from the judge; efficiency is computed.
type ScoreComponents struct {
	Accuracy   Points
	Simplicity Points
	Creativity Points
	Adherence  Points
	Efficiency Points
}

// ScoreWeights weigh the components into the total. They need not sum to 1:
// the total is normalized by their sum.
type ScoreWeights struct {
	Accuracy, Simplicity, Creativity, Adherence, Efficiency float64
}

// DefaultScoreWeights weighs every component equally.
var DefaultScoreWeights = ScoreWeights{Accuracy: 1, Simplicity: 1, Creativity: 1, Adherence: 1, Efficiency: 1}

func (w ScoreWeights) sum() float64 {
	return w.Accuracy + w.Simplicity + w.Creativity + w.Adherence + w.Efficiency
}

// Valid reports whether no weight is negative and at least one is positive.
func (w ScoreWeights) Valid() bool {
	return w.Accuracy >= 0 && w.Simplicity >= 0 && w.Creativity >= 0 && w.Adherence >= 0 && w.Efficiency >= 0 && w.sum() > 0
}

// Total is the weighted mean of the components, on the same scale, rounded
// to the nearest hundredth.
func Total(c ScoreComponents, w ScoreWeights) Points {
	weighted := w.Accuracy*float64(c.Accuracy) + w.Simplicity*float64(c.Simplicity) +
		w.Creativity*float64(c.Creativity) + w.Adherence*float64(c.Adherence) + w.Efficiency*float64(c.Efficiency)
	return min(max(Points(math.Floor(weighted/w.sum()+0.5)), 0), MaxPoints)
}

// Efficiency rewards needing few tests and a short prompt. It is the mean of
// two sub-scores on the 0..ScoreScale scale:
//
//   - tests: full marks for one test, EfficiencyTestPenalty less per extra
//     test, never below zero;
//   - length: full marks up to EfficiencyIdealLength code points, falling
//     linearly to zero at EfficiencyMaxLength.
const (
	EfficiencyTestPenalty = 1.0
	EfficiencyIdealLength = 100
	EfficiencyMaxLength   = 2000
)

func Efficiency(promptLength, testCount int) Points {
	tests := ScoreScale - EfficiencyTestPenalty*float64(max(testCount-1, 0))
	tests = max(tests, 0)

	length := float64(ScoreScale)
	if promptLength > EfficiencyIdealLength {
		over := float64(promptLength-EfficiencyIdealLength) / float64(EfficiencyMaxLength-EfficiencyIdealLength)
		length = ScoreScale * max(1-over, 0)
	}
	return PointsFrom((tests + length) / 2)
}

// PromptLength is the number of Unicode code points in the trimmed prompt.
func PromptLength(prompt string) int {
	return utf8.RuneCountInString(strings.TrimSpace(prompt))
}

// RankEntry is what ranking needs about one player's round.
type RankEntry struct {
	PlayerID     uuid.UUID
	NoSubmission bool
	Total        Points
	Accuracy     Points
	TestCount    int
	PromptLength int
	SubmittedAt  time.Time
}

// Rank orders a round's players and returns the rank of each entry, by index.
// Players are ordered by total (highest first), then higher accuracy, fewer
// tests, a shorter prompt and an earlier submission. Players tied on all of
// these share a rank, and the next rank skips accordingly (1, 1, 3). Players
// without a submission come last, sharing the rank after everyone else.
func Rank(entries []RankEntry) []int {
	order := make([]int, len(entries))
	for i := range order {
		order[i] = i
	}
	less := func(a, b RankEntry) bool {
		switch {
		case a.NoSubmission != b.NoSubmission:
			return !a.NoSubmission
		case a.NoSubmission:
			return false
		case a.Total != b.Total:
			return a.Total > b.Total
		case a.Accuracy != b.Accuracy:
			return a.Accuracy > b.Accuracy
		case a.TestCount != b.TestCount:
			return a.TestCount < b.TestCount
		case a.PromptLength != b.PromptLength:
			return a.PromptLength < b.PromptLength
		}
		return a.SubmittedAt.Before(b.SubmittedAt)
	}
	sort.SliceStable(order, func(i, j int) bool { return less(entries[order[i]], entries[order[j]]) })

	ranks := make([]int, len(entries))
	for pos, idx := range order {
		if pos > 0 {
			prev := order[pos-1]
			if !less(entries[prev], entries[idx]) && !less(entries[idx], entries[prev]) {
				ranks[idx] = ranks[prev]
				continue
			}
		}
		ranks[idx] = pos + 1
	}
	return ranks
}
