package dto

import (
	"time"

	"github.com/prompt-masters/backend/internal/domain"
)

type GameSettingsRequest struct {
	Rounds       int    `json:"rounds"`
	TimePerRound int    `json:"time_per_round"`
	Difficulty   string `json:"difficulty"`
	Category     string `json:"category"`
	AIModel      string `json:"ai_model"`
	MaxPlayers   int    `json:"max_players"`
}

type GameSettingsResponse struct {
	Rounds       int               `json:"rounds"`
	TimePerRound int               `json:"time_per_round"`
	Difficulty   domain.Difficulty `json:"difficulty"`
	Category     domain.Category   `json:"category"`
	AIModel      domain.AIModel    `json:"ai_model"`
	MaxPlayers   int               `json:"max_players"`
}

// GameSummaryResponse is a game as shown in listings, without its players.
type GameSummaryResponse struct {
	ID          string               `json:"id"`
	RoomCode    string               `json:"room_code"`
	HostID      string               `json:"host_id"`
	Status      domain.GameStatus    `json:"status"`
	Settings    GameSettingsResponse `json:"settings"`
	PlayerCount int                  `json:"player_count"`
	CreatedAt   time.Time            `json:"created_at"`
	UpdatedAt   time.Time            `json:"updated_at"`
	StartedAt   *time.Time           `json:"started_at"`
}

type GameResponse struct {
	GameSummaryResponse
	Players []GamePlayerResponse `json:"players"`
}

type GamePlayerResponse struct {
	UserID   string    `json:"user_id"`
	Username string    `json:"username"`
	IsHost   bool      `json:"is_host"`
	JoinedAt time.Time `json:"joined_at"`
}

func GameSummaryFromDomain(g *domain.Game) GameSummaryResponse {
	return GameSummaryResponse{
		ID:          g.ID.String(),
		RoomCode:    g.RoomCode,
		HostID:      g.HostID.String(),
		Status:      g.Status,
		Settings:    settingsFromDomain(g.Settings),
		PlayerCount: g.PlayerCount,
		CreatedAt:   g.CreatedAt,
		UpdatedAt:   g.UpdatedAt,
		StartedAt:   g.StartedAt,
	}
}

func settingsFromDomain(s domain.GameSettings) GameSettingsResponse {
	return GameSettingsResponse{
		Rounds:       s.Rounds,
		TimePerRound: s.TimePerRoundSeconds,
		Difficulty:   s.Difficulty,
		Category:     s.Category,
		AIModel:      s.AIModel,
		MaxPlayers:   s.MaxPlayers,
	}
}

func GameSummariesFromDomain(games []*domain.Game) []GameSummaryResponse {
	out := make([]GameSummaryResponse, len(games))
	for i, g := range games {
		out[i] = GameSummaryFromDomain(g)
	}
	return out
}

func GameFromDomain(g *domain.Game) GameResponse {
	players := make([]GamePlayerResponse, len(g.Players))
	for i, p := range g.Players {
		players[i] = GamePlayerResponse{
			UserID:   p.UserID.String(),
			Username: p.Username,
			IsHost:   g.IsHost(p.UserID),
			JoinedAt: p.JoinedAt,
		}
	}
	return GameResponse{GameSummaryResponse: GameSummaryFromDomain(g), Players: players}
}
