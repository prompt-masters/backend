package domain

import (
	"testing"
	"time"
)

func TestRoundStateRemaining(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		deadline time.Time
		want     time.Duration
	}{
		{name: "time left", deadline: now.Add(42 * time.Second), want: 42 * time.Second},
		{name: "exactly at deadline", deadline: now, want: 0},
		{name: "past deadline is clamped to zero", deadline: now.Add(-time.Minute), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (RoundState{Deadline: tt.deadline}).Remaining(now); got != tt.want {
				t.Errorf("Remaining() = %s, want %s", got, tt.want)
			}
		})
	}
}
