package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

// LiveStateSyncer mirrors committed lobby changes into live state. It is best
// effort: failures are logged, never returned, because PostgreSQL remains the
// source of truth and live state can be rebuilt from it.
type LiveStateSyncer interface {
	SyncGame(ctx context.Context, game *domain.Game)
}

// LiveStateService serves live game state from Redis to players and the game
// engine. The acting user ID always comes from the authenticated request.
//
// Membership is checked against the Redis roster. When Redis says a user is
// not a player, PostgreSQL is consulted: if the user is in fact a player the
// roster was stale (a best-effort sync failed), so it is re-synced and the
// operation retried once.
//
// When Redis holds no state for a game, the lobby and roster are rebuilt from
// PostgreSQL (and written back for active games). Readiness, the current
// round, drafts and points only ever live in Redis and come back empty; the
// result is marked Rebuilt. When Redis is unavailable rather than empty,
// domain.ErrStateUnavailable is returned instead.
type LiveStateService struct {
	state  repository.GameStateRepository
	tx     repository.Transactor
	logger *log.Logger
	now    func() time.Time
}

func NewLiveStateService(state repository.GameStateRepository, tx repository.Transactor, logger *log.Logger, now func() time.Time) *LiveStateService {
	return &LiveStateService{state: state, tx: tx, logger: logger, now: now}
}

var _ LiveStateSyncer = (*LiveStateService)(nil)

// GetLiveState rebuilds everything a reconnecting player needs: lobby,
// roster, readiness, current round with its remaining time, their own draft
// and points. Only players of the game may read it.
func (s *LiveStateService) GetLiveState(ctx context.Context, userID uuid.UUID, ref string) (*domain.LiveGameState, error) {
	gameID, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}

	live, err := s.state.Load(ctx, gameID, userID)
	switch {
	case errors.Is(err, domain.ErrStateNotFound):
		return s.rebuild(ctx, gameID, userID)
	case err != nil:
		return nil, s.logged("load", gameID, userID, err)
	}

	if !hasLivePlayer(live.Players, userID) {
		// The roster may be stale; PostgreSQL decides.
		if err := s.healMembership(ctx, gameID, userID); err != nil {
			return nil, err
		}
		if live, err = s.state.Load(ctx, gameID, userID); err != nil {
			return nil, s.logged("load", gameID, userID, err)
		}
	}

	if live.Round != nil {
		live.RoundRemaining = live.Round.Remaining(s.now())
	}
	return live, nil
}

// SetReady marks the user ready or not ready in a waiting game.
func (s *LiveStateService) SetReady(ctx context.Context, userID uuid.UUID, ref string, ready bool) error {
	gameID, lobby, err := s.lobby(ctx, userID, ref)
	if err != nil {
		return err
	}
	if lobby.Status != domain.GameStatusWaiting {
		return domain.ErrGameNotWaiting
	}

	op, write := "unset_ready", s.state.UnsetReady
	if ready {
		op, write = "set_ready", s.state.SetReady
	}
	return s.asPlayer(ctx, op, gameID, userID, func() error {
		return write(ctx, gameID, userID)
	})
}

// SaveDraft stores the user's draft prompt for the round in play.
func (s *LiveStateService) SaveDraft(ctx context.Context, userID uuid.UUID, ref, content string) (*domain.Draft, error) {
	if msg := validateDraft(content); msg != "" {
		return nil, &ValidationError{Fields: map[string]string{"content": msg}}
	}
	gameID, lobby, err := s.lobby(ctx, userID, ref)
	if err != nil {
		return nil, err
	}
	if lobby.Status != domain.GameStatusInProgress {
		return nil, domain.ErrGameNotInProgress
	}

	draft := domain.Draft{Content: content, UpdatedAt: s.now()}
	round, err := s.state.GetRound(ctx, gameID)
	switch {
	case err == nil:
		draft.RoundNumber = round.Number
	case !errors.Is(err, domain.ErrStateNotFound):
		return nil, s.logged("get_round", gameID, userID, err)
	}

	err = s.asPlayer(ctx, "save_draft", gameID, userID, func() error {
		return s.state.SaveDraft(ctx, gameID, userID, draft)
	})
	if err != nil {
		return nil, err
	}
	return &draft, nil
}

// GetDraft returns the user's own draft. The key is built from userID, so no
// other player's draft is reachable; a user without a draft, including a
// non-player, gets domain.ErrStateNotFound.
func (s *LiveStateService) GetDraft(ctx context.Context, userID uuid.UUID, ref string) (*domain.Draft, error) {
	gameID, err := s.resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	draft, err := s.state.GetDraft(ctx, gameID, userID)
	if err != nil && !errors.Is(err, domain.ErrStateNotFound) {
		return nil, s.logged("get_draft", gameID, userID, err)
	}
	return draft, err
}

// StartRound records the round in play. The deadline is computed here from
// the game's time per round; callers cannot set it.
func (s *LiveStateService) StartRound(ctx context.Context, gameID uuid.UUID, number int, challengeID uuid.UUID) (*domain.RoundState, error) {
	lobby, err := s.lobbyByID(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if lobby.Status != domain.GameStatusInProgress {
		return nil, domain.ErrGameNotInProgress
	}
	if number < 1 || number > lobby.Settings.Rounds {
		return nil, fmt.Errorf("round %d is outside 1..%d", number, lobby.Settings.Rounds)
	}

	now := s.now()
	round := domain.RoundState{
		Number:      number,
		ChallengeID: challengeID,
		StartedAt:   now,
		Deadline:    now.Add(time.Duration(lobby.Settings.TimePerRoundSeconds) * time.Second),
	}
	if err := s.state.SetRound(ctx, gameID, round); err != nil {
		return nil, s.logged("set_round", gameID, uuid.Nil, err)
	}
	return &round, nil
}

// AwardPoints atomically adds delta to a player's helper points.
func (s *LiveStateService) AwardPoints(ctx context.Context, gameID, userID uuid.UUID, delta int64) (int64, error) {
	var total int64
	err := s.asPlayer(ctx, "award_points", gameID, userID, func() error {
		var err error
		total, err = s.state.IncrementPoints(ctx, gameID, userID, delta)
		return err
	})
	return total, err
}

// ClearGame removes all live state of a game.
func (s *LiveStateService) ClearGame(ctx context.Context, gameID uuid.UUID) error {
	if err := s.state.Delete(ctx, gameID); err != nil {
		return s.logged("delete", gameID, uuid.Nil, err)
	}
	return nil
}

// SyncGame writes the game's lobby and roster to live state. Failures are
// logged and swallowed. The write survives the caller's context being
// cancelled, since the change it mirrors has already been committed.
func (s *LiveStateService) SyncGame(ctx context.Context, game *domain.Game) {
	ctx = context.WithoutCancel(ctx)
	if err := s.state.SyncLobby(ctx, lobbyFromGame(game, s.now()), livePlayersFromGame(game)); err != nil {
		s.logger.Printf("live_state op=sync game_id=%s status=%s error=%q", game.ID, game.Status, err)
	}
}

// lobby resolves ref and returns the game's lobby state, rebuilding it from
// PostgreSQL when Redis has none.
func (s *LiveStateService) lobby(ctx context.Context, userID uuid.UUID, ref string) (uuid.UUID, *domain.LobbyState, error) {
	gameID, err := s.resolve(ctx, ref)
	if err != nil {
		return uuid.Nil, nil, err
	}
	lobby, err := s.lobbyByID(ctx, gameID)
	if err != nil {
		return uuid.Nil, nil, err
	}
	return gameID, lobby, nil
}

func (s *LiveStateService) lobbyByID(ctx context.Context, gameID uuid.UUID) (*domain.LobbyState, error) {
	lobby, err := s.state.GetLobby(ctx, gameID)
	if !errors.Is(err, domain.ErrStateNotFound) {
		if err != nil {
			return nil, s.logged("get_lobby", gameID, uuid.Nil, err)
		}
		return lobby, nil
	}

	game, err := s.game(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if game.Status.Active() {
		s.SyncGame(ctx, game)
	}
	rebuilt := lobbyFromGame(game, s.now())
	return &rebuilt, nil
}

// asPlayer runs a write that the store rejects for non-players, healing a
// stale roster once.
func (s *LiveStateService) asPlayer(ctx context.Context, op string, gameID, userID uuid.UUID, write func() error) error {
	err := write()
	if errors.Is(err, domain.ErrNotGamePlayer) {
		if err := s.healMembership(ctx, gameID, userID); err != nil {
			return err
		}
		err = write()
	}
	if err != nil && !errors.Is(err, domain.ErrNotGamePlayer) {
		return s.logged(op, gameID, userID, err)
	}
	return err
}

// healMembership checks PostgreSQL. Non-players get domain.ErrNotGamePlayer;
// for players of an active game the live roster is re-synced.
func (s *LiveStateService) healMembership(ctx context.Context, gameID, userID uuid.UUID) error {
	game, err := s.game(ctx, gameID)
	if err != nil {
		return err
	}
	if !game.HasPlayer(userID) {
		return domain.ErrNotGamePlayer
	}
	s.logger.Printf("live_state op=heal_roster game_id=%s user_id=%s", gameID, userID)
	s.SyncGame(ctx, game)
	return nil
}

// rebuild answers a reconnect from PostgreSQL when Redis has no state.
func (s *LiveStateService) rebuild(ctx context.Context, gameID, userID uuid.UUID) (*domain.LiveGameState, error) {
	game, err := s.game(ctx, gameID)
	if err != nil {
		return nil, err
	}
	if !game.HasPlayer(userID) {
		return nil, domain.ErrNotGamePlayer
	}
	if game.Status.Active() {
		s.SyncGame(ctx, game)
	}
	return &domain.LiveGameState{
		Lobby:   lobbyFromGame(game, s.now()),
		Players: livePlayersFromGame(game),
		Ready:   []uuid.UUID{},
		Points:  map[uuid.UUID]int64{},
		Rebuilt: true,
	}, nil
}

func (s *LiveStateService) game(ctx context.Context, gameID uuid.UUID) (*domain.Game, error) {
	var game *domain.Game
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		game, err = repos.Games.GetByID(ctx, gameID)
		return err
	})
	return game, err
}

// resolve turns a game UUID or active room code into the game's ID. UUIDs are
// used directly; room codes are looked up in PostgreSQL.
func (s *LiveStateService) resolve(ctx context.Context, ref string) (uuid.UUID, error) {
	if !domain.IsRoomCode(ref) {
		id, err := uuid.Parse(ref)
		if err != nil || id == uuid.Nil {
			return uuid.Nil, domain.ErrNotFound
		}
		return id, nil
	}
	var id uuid.UUID
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		id, err = resolveGameID(ctx, repos.Games, ref)
		return err
	})
	return id, err
}

// logged records live state failures that are not ordinary outcomes. Values
// such as draft content are never logged.
func (s *LiveStateService) logged(op string, gameID, userID uuid.UUID, err error) error {
	if errors.Is(err, domain.ErrNotGamePlayer) || errors.Is(err, domain.ErrStateNotFound) {
		return err
	}
	s.logger.Printf("live_state op=%s game_id=%s user_id=%s unavailable=%t error=%q",
		op, gameID, userID, errors.Is(err, domain.ErrStateUnavailable), err)
	return err
}

func validateDraft(content string) string {
	switch {
	case strings.TrimSpace(content) == "":
		return "is required"
	case len(content) > domain.MaxDraftBytes:
		return fmt.Sprintf("must not exceed %d bytes", domain.MaxDraftBytes)
	case !utf8.ValidString(content):
		return "must be valid UTF-8"
	}
	return ""
}

func hasLivePlayer(players []domain.LivePlayer, userID uuid.UUID) bool {
	for _, p := range players {
		if p.UserID == userID {
			return true
		}
	}
	return false
}

func lobbyFromGame(g *domain.Game, now time.Time) domain.LobbyState {
	return domain.LobbyState{
		GameID:    g.ID,
		RoomCode:  g.RoomCode,
		HostID:    g.HostID,
		Status:    g.Status,
		Settings:  g.Settings,
		UpdatedAt: now,
	}
}

func livePlayersFromGame(g *domain.Game) []domain.LivePlayer {
	players := make([]domain.LivePlayer, len(g.Players))
	for i, p := range g.Players {
		players[i] = domain.LivePlayer{UserID: p.UserID, Username: p.Username, JoinedAt: p.JoinedAt}
	}
	return players
}
