package dto

import (
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type DraftRequest struct {
	Content string `json:"content"`
}

type DraftResponse struct {
	Content     string    `json:"content"`
	RoundNumber int       `json:"round_number"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type LivePlayerResponse struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	IsHost   bool      `json:"is_host"`
	IsReady  bool      `json:"is_ready"`
	Points   int64     `json:"points"`
	JoinedAt time.Time `json:"joined_at"`
}

type RoundResponse struct {
	Number      int       `json:"number"`
	ChallengeID string    `json:"challenge_id"`
	StartedAt   time.Time `json:"started_at"`
	Deadline    time.Time `json:"deadline"`
	// RemainingMs is computed by the server from Deadline.
	RemainingMs int64 `json:"remaining_ms"`
}

type LiveStateResponse struct {
	GameID   string               `json:"game_id"`
	RoomCode string               `json:"room_code"`
	HostID   string               `json:"host_id"`
	Status   domain.GameStatus    `json:"status"`
	Settings GameSettingsResponse `json:"settings"`
	Players  []LivePlayerResponse `json:"players"`
	Round    *RoundResponse       `json:"round"`
	// Draft is the requesting player's own draft.
	Draft *DraftResponse `json:"draft"`
	// Rebuilt is true when Redis held no state and it was rebuilt from the
	// database; readiness, round, drafts and points are then empty.
	Rebuilt bool `json:"rebuilt"`

	// The game engine's state, as in the game_state event. Timestamps are
	// UTC Unix milliseconds; the round fields are omitted before round 1.
	State          domain.GamePhase `json:"state"`
	CurrentRound   int              `json:"current_round"`
	TotalRounds    int              `json:"total_rounds"`
	RoundID        string           `json:"round_id,omitempty"`
	RoundStartedAt int64            `json:"round_started_at,omitempty"`
	RoundDeadline  int64            `json:"round_deadline,omitempty"`
	ServerTime     int64            `json:"server_time"`
}

func DraftFromDomain(d *domain.Draft) DraftResponse {
	return DraftResponse{Content: d.Content, RoundNumber: d.RoundNumber, UpdatedAt: d.UpdatedAt}
}

func LiveStateFromDomain(s *domain.LiveGameState) LiveStateResponse {
	ready := make(map[string]bool, len(s.Ready))
	for _, id := range s.Ready {
		ready[id.String()] = true
	}

	players := make([]LivePlayerResponse, len(s.Players))
	for i, p := range s.Players {
		players[i] = LivePlayerResponse{
			UserID:   p.UserID.String(),
			Username: p.Username,
			IsHost:   p.UserID == s.Lobby.HostID,
			IsReady:  ready[p.UserID.String()],
			Points:   s.Points[p.UserID],
			JoinedAt: p.JoinedAt,
		}
	}

	resp := LiveStateResponse{
		GameID:   s.Lobby.GameID.String(),
		RoomCode: s.Lobby.RoomCode,
		HostID:   s.Lobby.HostID.String(),
		Status:   s.Lobby.Status,
		Settings: settingsFromDomain(s.Lobby.Settings),
		Players:  players,
		Rebuilt:  s.Rebuilt,

		State:        s.Progress.Phase,
		CurrentRound: s.Progress.CurrentRound,
		TotalRounds:  s.Progress.TotalRounds,
		ServerTime:   s.ServerTime.UnixMilli(),
	}
	if s.Progress.RoundID != uuid.Nil {
		resp.RoundID = s.Progress.RoundID.String()
		resp.RoundStartedAt = s.Progress.RoundStartedAt.UnixMilli()
		resp.RoundDeadline = s.Progress.RoundDeadline.UnixMilli()
	}
	if s.Round != nil {
		resp.Round = &RoundResponse{
			Number:      s.Round.Number,
			ChallengeID: s.Round.ChallengeID.String(),
			StartedAt:   s.Round.StartedAt,
			Deadline:    s.Round.Deadline,
			RemainingMs: s.RoundRemaining.Milliseconds(),
		}
	}
	if s.Draft != nil {
		d := DraftFromDomain(s.Draft)
		resp.Draft = &d
	}
	return resp
}
