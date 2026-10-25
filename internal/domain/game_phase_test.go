package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestGameProgressNextCoversEveryPair(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	oldRound := uuid.New()

	// progressIn builds a game in phase whose round deadline has passed and
	// which has rounds left, so that every guard allows its transition.
	progressIn := func(phase GamePhase) GameProgress {
		p := GameProgress{Phase: phase, TotalRounds: 3}
		if phase != GamePhaseLobby {
			p.CurrentRound = 2
			p.RoundID = oldRound
			p.RoundStartedAt = now.Add(-time.Minute)
			p.RoundDeadline = now.Add(-time.Second)
		}
		return p
	}

	valid := map[GamePhase]map[GameEvent]GamePhase{
		GamePhaseLobby:       {GameEventStartGame: GamePhaseRoundActive},
		GamePhaseRoundActive: {GameEventAllSubmitted: GamePhaseJudging, GameEventTimerExpired: GamePhaseJudging},
		GamePhaseJudging:     {GameEventJudgeCompleted: GamePhaseResults},
		// finalRound is valid from Results but guarded; see the guard tests.
		GamePhaseResults: {GameEventNextRound: GamePhaseRoundActive},
	}
	guardedOut := map[GamePhase]GameEvent{GamePhaseResults: GameEventFinalRound}

	for _, phase := range GamePhases() {
		for _, event := range GameEvents() {
			t.Run(string(phase)+"/"+string(event), func(t *testing.T) {
				from := progressIn(phase)
				got, err := from.Next(event, now, RoundPlan{ID: uuid.New(), Length: time.Minute})

				want, ok := valid[phase][event]
				if !ok {
					if !errors.Is(err, ErrInvalidTransition) {
						t.Fatalf("Next() error = %v, want ErrInvalidTransition", err)
					}
					if got != from {
						t.Errorf("Next() = %+v after a rejected event, want the progress unchanged %+v", got, from)
					}
					if guardedOut[phase] != event && transitions[phase][event].to != "" {
						t.Errorf("%s/%s is in the table but not expected to be valid", phase, event)
					}
					return
				}
				if err != nil {
					t.Fatalf("Next() error = %v, want a transition to %s", err, want)
				}
				if got.Phase != want {
					t.Errorf("phase = %s, want %s", got.Phase, want)
				}
			})
		}
	}
}

func TestGameProgressNextGuards(t *testing.T) {
	now := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)

	tests := []struct {
		name      string
		from      GameProgress
		event     GameEvent
		wantPhase GamePhase
		wantErr   bool
	}{
		{
			name:  "timer before the deadline",
			from:  GameProgress{Phase: GamePhaseRoundActive, CurrentRound: 1, TotalRounds: 3, RoundDeadline: now.Add(time.Millisecond)},
			event: GameEventTimerExpired, wantErr: true,
		},
		{
			name:  "timer exactly at the deadline",
			from:  GameProgress{Phase: GamePhaseRoundActive, CurrentRound: 1, TotalRounds: 3, RoundDeadline: now},
			event: GameEventTimerExpired, wantPhase: GamePhaseJudging,
		},
		{
			name:  "next round while rounds remain",
			from:  GameProgress{Phase: GamePhaseResults, CurrentRound: 2, TotalRounds: 3},
			event: GameEventNextRound, wantPhase: GamePhaseRoundActive,
		},
		{
			name:  "next round after the last",
			from:  GameProgress{Phase: GamePhaseResults, CurrentRound: 3, TotalRounds: 3},
			event: GameEventNextRound, wantErr: true,
		},
		{
			name:  "final round after the last",
			from:  GameProgress{Phase: GamePhaseResults, CurrentRound: 3, TotalRounds: 3},
			event: GameEventFinalRound, wantPhase: GamePhaseGameOver,
		},
		{
			name:  "final round while rounds remain",
			from:  GameProgress{Phase: GamePhaseResults, CurrentRound: 2, TotalRounds: 3},
			event: GameEventFinalRound, wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.from.Next(tt.event, now, RoundPlan{ID: uuid.New(), Length: time.Minute})
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("Next() error = %v, want ErrInvalidTransition", err)
				}
				if got != tt.from {
					t.Errorf("Next() = %+v, want the progress unchanged", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Next() error = %v", err)
			}
			if got.Phase != tt.wantPhase {
				t.Errorf("phase = %s, want %s", got.Phase, tt.wantPhase)
			}
		})
	}
}

func TestGameProgressNextStartsARound(t *testing.T) {
	// Sub-millisecond precision is dropped: timing is stored in milliseconds.
	now := time.Date(2026, 9, 29, 10, 0, 0, 123_456_789, time.FixedZone("UTC+7", 7*3600))
	plan := RoundPlan{ID: uuid.New(), Length: 90 * time.Second}

	for _, tt := range []struct {
		name      string
		from      GameProgress
		event     GameEvent
		wantRound int
	}{
		{name: "first round", from: GameProgress{Phase: GamePhaseLobby, TotalRounds: 3}, event: GameEventStartGame, wantRound: 1},
		{name: "next round", from: GameProgress{Phase: GamePhaseResults, CurrentRound: 1, TotalRounds: 3, RoundID: uuid.New()}, event: GameEventNextRound, wantRound: 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.from.Next(tt.event, now, plan)
			if err != nil {
				t.Fatal(err)
			}
			wantStart := time.Date(2026, 9, 29, 3, 0, 0, 123_000_000, time.UTC)
			if got.CurrentRound != tt.wantRound || got.RoundID != plan.ID {
				t.Errorf("round = %d (%s), want %d (%s)", got.CurrentRound, got.RoundID, tt.wantRound, plan.ID)
			}
			if !got.RoundStartedAt.Equal(wantStart) || got.RoundStartedAt.Location() != time.UTC {
				t.Errorf("started at = %v, want %v in UTC", got.RoundStartedAt, wantStart)
			}
			if !got.RoundDeadline.Equal(wantStart.Add(plan.Length)) {
				t.Errorf("deadline = %v, want %v", got.RoundDeadline, wantStart.Add(plan.Length))
			}
		})
	}

	t.Run("leaving a round keeps its identity", func(t *testing.T) {
		from := GameProgress{Phase: GamePhaseRoundActive, CurrentRound: 2, TotalRounds: 3, RoundID: uuid.New(), RoundDeadline: now}
		got, err := from.Next(GameEventAllSubmitted, now, plan)
		if err != nil {
			t.Fatal(err)
		}
		if got.RoundID != from.RoundID || got.CurrentRound != 2 || got.RoundDeadline != from.RoundDeadline {
			t.Errorf("Next() = %+v, want the round kept from %+v", got, from)
		}
	})
}

func TestGameProgressAcceptsSubmissions(t *testing.T) {
	deadline := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	active := GameProgress{Phase: GamePhaseRoundActive, RoundDeadline: deadline}

	if !active.AcceptsSubmissions(deadline.Add(-time.Millisecond)) {
		t.Error("rejected a submission before the deadline")
	}
	if active.AcceptsSubmissions(deadline) {
		t.Error("accepted a submission at the deadline")
	}
	for _, phase := range []GamePhase{GamePhaseLobby, GamePhaseJudging, GamePhaseResults, GamePhaseGameOver} {
		p := active
		p.Phase = phase
		if p.AcceptsSubmissions(deadline.Add(-time.Minute)) {
			t.Errorf("accepted a submission in %s", phase)
		}
	}
}

func TestGamePhaseStatus(t *testing.T) {
	want := map[GamePhase]GameStatus{
		GamePhaseLobby:       GameStatusWaiting,
		GamePhaseRoundActive: GameStatusInProgress,
		GamePhaseJudging:     GameStatusInProgress,
		GamePhaseResults:     GameStatusInProgress,
		GamePhaseGameOver:    GameStatusFinished,
	}
	for _, phase := range GamePhases() {
		if got := phase.Status(); got != want[phase] {
			t.Errorf("%s.Status() = %s, want %s", phase, got, want[phase])
		}
	}
}
