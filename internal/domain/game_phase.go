package domain

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// GamePhase is where a game stands in the game engine's state machine. It
// refines GameStatus: see Status.
type GamePhase string

const (
	GamePhaseLobby       GamePhase = "Lobby"
	GamePhaseRoundActive GamePhase = "RoundActive"
	GamePhaseJudging     GamePhase = "Judging"
	GamePhaseResults     GamePhase = "Results"
	GamePhaseGameOver    GamePhase = "GameOver"
)

func GamePhases() []GamePhase {
	return []GamePhase{GamePhaseLobby, GamePhaseRoundActive, GamePhaseJudging, GamePhaseResults, GamePhaseGameOver}
}

// Status is the lifecycle status that goes with the phase. A cancelled game
// keeps the phase it was cancelled in, so cancellation is read from the
// status, never from the phase.
func (p GamePhase) Status() GameStatus {
	switch p {
	case GamePhaseLobby:
		return GameStatusWaiting
	case GamePhaseGameOver:
		return GameStatusFinished
	}
	return GameStatusInProgress
}

// GameEvent is an input to the game engine's state machine.
type GameEvent string

const (
	GameEventStartGame      GameEvent = "startGame"
	GameEventAllSubmitted   GameEvent = "allSubmitted"
	GameEventTimerExpired   GameEvent = "timerExpired"
	GameEventJudgeCompleted GameEvent = "judgeCompleted"
	GameEventNextRound      GameEvent = "nextRound"
	GameEventFinalRound     GameEvent = "finalRound"
)

func GameEvents() []GameEvent {
	return []GameEvent{
		GameEventStartGame, GameEventAllSubmitted, GameEventTimerExpired,
		GameEventJudgeCompleted, GameEventNextRound, GameEventFinalRound,
	}
}

// GameProgress is a game's position in the state machine. Round timing is set
// by the server alone, and is kept at millisecond precision because that is
// how it is stored and sent.
type GameProgress struct {
	Phase GamePhase
	// CurrentRound is 0 in the lobby, then 1..TotalRounds.
	CurrentRound int
	TotalRounds  int
	// RoundID identifies the current (or last) round; uuid.Nil before the
	// first. Events that belong to a round carry it, so a late event for an
	// earlier round can be recognised and ignored.
	RoundID        uuid.UUID
	RoundStartedAt time.Time
	RoundDeadline  time.Time
	// ChallengeID is the current (or last) round's challenge.
	ChallengeID uuid.UUID
}

// AcceptsSubmissions reports whether a submission made at now belongs to the
// round in play.
func (p GameProgress) AcceptsSubmissions(now time.Time) bool {
	return p.Phase == GamePhaseRoundActive && now.Before(p.RoundDeadline)
}

// RoundPlan is what entering a new round needs: its ID, its length and its
// challenge.
type RoundPlan struct {
	ID          uuid.UUID
	Length      time.Duration
	ChallengeID uuid.UUID
}

type transition struct {
	to GamePhase
	// guard, when set, explains why the transition may not happen now.
	guard func(p GameProgress, now time.Time) string
}

// transitions is the complete state machine: a (phase, event) pair missing
// from it is not a transition. A table fits because no phase has behaviour of
// its own beyond where each event leads and when it is allowed.
var transitions = map[GamePhase]map[GameEvent]transition{
	GamePhaseLobby: {
		GameEventStartGame: {to: GamePhaseRoundActive},
	},
	GamePhaseRoundActive: {
		GameEventAllSubmitted: {to: GamePhaseJudging},
		GameEventTimerExpired: {to: GamePhaseJudging, guard: func(p GameProgress, now time.Time) string {
			if now.Before(p.RoundDeadline) {
				return "the round deadline has not passed"
			}
			return ""
		}},
	},
	GamePhaseJudging: {
		GameEventJudgeCompleted: {to: GamePhaseResults},
	},
	GamePhaseResults: {
		GameEventNextRound: {to: GamePhaseRoundActive, guard: func(p GameProgress, _ time.Time) string {
			if p.CurrentRound >= p.TotalRounds {
				return "no rounds remain"
			}
			return ""
		}},
		GameEventFinalRound: {to: GamePhaseGameOver, guard: func(p GameProgress, _ time.Time) string {
			if p.CurrentRound != p.TotalRounds {
				return "rounds remain"
			}
			return ""
		}},
	},
}

// Next returns the progress that event leads to at now, or an error wrapping
// ErrInvalidTransition. It is pure: p is never modified, and plan is only used
// when the event starts a new round.
func (p GameProgress) Next(event GameEvent, now time.Time, plan RoundPlan) (GameProgress, error) {
	t, ok := transitions[p.Phase][event]
	if !ok {
		return p, fmt.Errorf("%w: %s in %s", ErrInvalidTransition, event, p.Phase)
	}
	if t.guard != nil {
		if reason := t.guard(p, now); reason != "" {
			return p, fmt.Errorf("%w: %s in %s: %s", ErrInvalidTransition, event, p.Phase, reason)
		}
	}

	next := p
	next.Phase = t.to
	if t.to == GamePhaseRoundActive {
		started := now.UTC().Truncate(time.Millisecond)
		next.CurrentRound++
		next.RoundID = plan.ID
		next.RoundStartedAt = started
		next.RoundDeadline = started.Add(plan.Length)
		next.ChallengeID = plan.ChallengeID
	}
	return next, nil
}
