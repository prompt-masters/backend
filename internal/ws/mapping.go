package ws

import (
	"time"

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
	payload.setProgress(live.Progress, live.ServerTime)
	if live.Results != nil {
		results := RoundResultsFrom(*live.Results)
		payload.RoundResults = &results
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
// game, without any Redis-only data, stamped with the server time now.
func GameStateFromGame(game *domain.Game, now time.Time) GameStatePayload {
	players := make([]PlayerInfo, len(game.Players))
	for i, p := range game.Players {
		players[i] = PlayerInfo{
			UserID:   p.UserID.String(),
			Username: p.Username,
			IsHost:   p.UserID == game.HostID,
			JoinedAt: p.JoinedAt,
		}
	}
	payload := GameStatePayload{
		Game: GameInfo{
			GameID:   game.ID.String(),
			RoomCode: game.RoomCode,
			HostID:   game.HostID.String(),
			Status:   string(game.Status),
			Settings: settingsInfo(game.Settings),
		},
		Players: players,
	}
	payload.setProgress(game.Progress, now)
	return payload
}

func (p *GameStatePayload) setProgress(progress domain.GameProgress, now time.Time) {
	p.State = string(progress.Phase)
	p.CurrentRound = progress.CurrentRound
	p.TotalRounds = progress.TotalRounds
	p.ServerTime = now.UnixMilli()
	if progress.ChallengeID != uuid.Nil {
		p.ChallengeID = progress.ChallengeID.String()
	}
	if progress.RoundID != uuid.Nil {
		p.RoundID = progress.RoundID.String()
		p.RoundStartedAt = progress.RoundStartedAt.UnixMilli()
		p.RoundDeadline = progress.RoundDeadline.UnixMilli()
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

// RoundResultsFrom maps a round's published results.
func RoundResultsFrom(r domain.RoundResults) RoundResultsPayload {
	entries := make([]RoundResultEntry, len(r.Results))
	for i, res := range r.Results {
		e := RoundResultEntry{
			Rank:        res.Rank,
			PlayerID:    res.PlayerID.String(),
			DisplayName: res.DisplayName,
			Prompt:      res.Prompt,
			Output:      res.Output,
			Scores: ScoresInfo{
				Accuracy:   res.Components.Accuracy.Float(),
				Simplicity: res.Components.Simplicity.Float(),
				Creativity: res.Components.Creativity.Float(),
				Adherence:  res.Components.Adherence.Float(),
				Efficiency: res.Components.Efficiency.Float(),
				Total:      res.Total.Float(),
			},
			PromptLength: res.PromptLength,
			TestCount:    res.TestCount,
			Source:       string(res.Source),
			Rationale:    res.Rationale,
			NoSubmission: res.NoSubmission,
		}
		if !res.NoSubmission {
			e.SubmissionID = res.SubmissionID.String()
		}
		entries[i] = e
	}
	return RoundResultsPayload{RoundID: r.RoundID.String(), Round: r.Number, Results: entries}
}
