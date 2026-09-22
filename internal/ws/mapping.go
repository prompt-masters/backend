package ws

import (
	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

// GameStateFrom builds a game_state payload from live state. The caller's own
// draft is included only when withDraft is set, which must never be true for a
// room broadcast.
func GameStateFrom(live *domain.LiveGameState, withDraft bool) GameStatePayload {
	ready := make(map[uuid.UUID]bool, len(live.Ready))
	for _, id := range live.Ready {
		ready[id] = true
	}

	players := make([]PlayerInfo, len(live.Players))
	for i, p := range live.Players {
		players[i] = PlayerInfo{
			UserID:   p.UserID.String(),
			Username: p.Username,
			IsHost:   p.UserID == live.Lobby.HostID,
			IsReady:  ready[p.UserID],
			Points:   live.Points[p.UserID],
			JoinedAt: p.JoinedAt,
		}
	}

	payload := GameStatePayload{
		Game: GameInfo{
			GameID:   live.Lobby.GameID.String(),
			RoomCode: live.Lobby.RoomCode,
			HostID:   live.Lobby.HostID.String(),
			Status:   string(live.Lobby.Status),
			Settings: settingsInfo(live.Lobby.Settings),
		},
		Players: players,
		Rebuilt: live.Rebuilt,
	}
	if live.Round != nil {
		payload.Round = &RoundInfo{
			Number:      live.Round.Number,
			ChallengeID: live.Round.ChallengeID.String(),
			StartedAt:   live.Round.StartedAt,
			Deadline:    live.Round.Deadline,
			RemainingMs: live.RoundRemaining.Milliseconds(),
		}
	}
	if withDraft && live.Draft != nil {
		payload.YourDraft = &DraftInfo{
			Content:     live.Draft.Content,
			RoundNumber: live.Draft.RoundNumber,
			UpdatedAt:   live.Draft.UpdatedAt,
		}
	}
	return payload
}

// GameStateFromGame builds a game_state payload from the database view of a
// game, without any Redis-only data.
func GameStateFromGame(game *domain.Game) GameStatePayload {
	players := make([]PlayerInfo, len(game.Players))
	for i, p := range game.Players {
		players[i] = PlayerInfo{
			UserID:   p.UserID.String(),
			Username: p.Username,
			IsHost:   p.UserID == game.HostID,
			JoinedAt: p.JoinedAt,
		}
	}
	return GameStatePayload{
		Game: GameInfo{
			GameID:   game.ID.String(),
			RoomCode: game.RoomCode,
			HostID:   game.HostID.String(),
			Status:   string(game.Status),
			Settings: settingsInfo(game.Settings),
		},
		Players: players,
	}
}

// PlayerJoinedFrom describes a player who just joined game.
func PlayerJoinedFrom(game *domain.Game, userID uuid.UUID) PlayerJoinedPayload {
	info := PlayerInfo{UserID: userID.String(), IsHost: game.HostID == userID}
	for _, p := range game.Players {
		if p.UserID == userID {
			info.Username, info.JoinedAt = p.Username, p.JoinedAt
			break
		}
	}
	return PlayerJoinedPayload{Player: info, PlayerCount: game.PlayerCount}
}

// PlayerLeftFrom describes a player who just left game, naming the new host
// when hosting moved.
func PlayerLeftFrom(game *domain.Game, userID uuid.UUID, username string, hostChanged bool) PlayerLeftPayload {
	payload := PlayerLeftPayload{UserID: userID.String(), Username: username, PlayerCount: game.PlayerCount}
	if hostChanged {
		payload.NewHostID = game.HostID.String()
	}
	return payload
}

// RoundStartFrom pairs the server's round timing with its challenge.
func RoundStartFrom(round domain.RoundState, remainingMs int64, challenge *domain.Challenge) RoundStartPayload {
	payload := RoundStartPayload{
		Round: RoundInfo{
			Number:      round.Number,
			ChallengeID: round.ChallengeID.String(),
			StartedAt:   round.StartedAt,
			Deadline:    round.Deadline,
			RemainingMs: remainingMs,
		},
	}
	if challenge != nil {
		payload.Challenge = ChallengeInfo{
			ChallengeID: challenge.ID.String(),
			Title:       challenge.Title,
			Description: challenge.Description,
			Category:    string(challenge.Category),
			Difficulty:  string(challenge.Difficulty),
			Constraints: challenge.Constraints,
		}
	}
	return payload
}

func settingsInfo(s domain.GameSettings) SettingsInfo {
	return SettingsInfo{
		Rounds:       s.Rounds,
		TimePerRound: s.TimePerRoundSeconds,
		Difficulty:   string(s.Difficulty),
		Category:     string(s.Category),
		AIModel:      string(s.AIModel),
		MaxPlayers:   s.MaxPlayers,
	}
}
