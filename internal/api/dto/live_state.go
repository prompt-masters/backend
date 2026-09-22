package dto

import (
	"time"

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
