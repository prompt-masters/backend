package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

const (
	// maxRoomCodeAttempts bounds retries when a generated room code is held by
	// another active game.
	maxRoomCodeAttempts = 10
	roomCodeSpace       = 1_000_000
	gameListLimit       = 100
)

type GameSettingsInput struct {
	Rounds       int
	TimePerRound int
	Difficulty   string
	Category     string
	AIModel      string
	MaxPlayers   int
}

// GameService manages game rooms. Every operation that changes a game runs in
// one transaction that first row-locks the game, so joins, leaves, settings
// changes, starts and cancels on the same game are serialized: a room cannot
// overfill and no change can land after the game has started.
//
// Game references accept either the game's UUID or the six-digit room code of
// an active game.
type GameService struct {
	tx     repository.Transactor
	random Random
}

// NewGameService uses random for room codes and for the ChallengeFactory that
// checks a game can start.
func NewGameService(tx repository.Transactor, random Random) *GameService {
	return &GameService{tx: tx, random: random}
}

// List returns up to gameListLimit games in the given status, newest first.
// A blank status lists waiting games.
func (s *GameService) List(ctx context.Context, status string) ([]*domain.Game, error) {
	st := domain.GameStatus(strings.ToLower(strings.TrimSpace(status)))
	if st == "" {
		st = domain.GameStatusWaiting
	}
	if !st.Valid() {
		return nil, &ValidationError{Fields: map[string]string{
			"status": "must be one of: " + joinValues(domain.GameStatuses()),
		}}
	}

	var games []*domain.Game
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		var err error
		games, err = repos.Games.ListByStatus(ctx, st, gameListLimit)
		return err
	})
	return games, err
}

// Create opens a waiting room hosted by hostID, who also becomes its first
// player.
func (s *GameService) Create(ctx context.Context, hostID uuid.UUID, in GameSettingsInput) (*domain.Game, error) {
	settings, err := parseGameSettings(in)
	if err != nil {
		return nil, err
	}

	var game *domain.Game
	err = s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		repo := repos.Games
		created, err := s.createWithUniqueRoomCode(ctx, repo, hostID)
		if err != nil {
			return err
		}
		if err := repo.CreateSettings(ctx, created.ID, settings); err != nil {
			return fmt.Errorf("storing game settings: %w", err)
		}
		if err := repo.AddPlayer(ctx, created.ID, hostID); err != nil {
			return fmt.Errorf("adding host as player: %w", err)
		}
		game, err = repo.GetByID(ctx, created.ID)
		return err
	})
	return game, err
}

func (s *GameService) Get(ctx context.Context, ref string) (*domain.Game, error) {
	var game *domain.Game
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		repo := repos.Games
		id, err := resolveGameID(ctx, repo, ref)
		if err != nil {
			return err
		}
		game, err = repo.GetByID(ctx, id)
		return err
	})
	return game, err
}

// UpdateSettings replaces the game's settings. Only the host may do so, only
// while the game is waiting, and max_players cannot drop below the current
// player count.
func (s *GameService) UpdateSettings(ctx context.Context, userID uuid.UUID, ref string, in GameSettingsInput) (*domain.Game, error) {
	settings, err := parseGameSettings(in)
	if err != nil {
		return nil, err
	}

	return s.mutate(ctx, ref, func(repos repository.TxRepositories, g *domain.Game) error {
		if !g.IsHost(userID) {
			return domain.ErrNotGameHost
		}
		if g.Status != domain.GameStatusWaiting {
			return domain.ErrGameNotWaiting
		}
		if settings.MaxPlayers < g.PlayerCount {
			return &ValidationError{Fields: map[string]string{
				"max_players": fmt.Sprintf("must not be less than the current player count (%d)", g.PlayerCount),
			}}
		}
		return repos.Games.UpdateSettings(ctx, g.ID, settings)
	})
}

// Join adds userID to a waiting game that has room for another player.
func (s *GameService) Join(ctx context.Context, userID uuid.UUID, ref string) (*domain.Game, error) {
	return s.mutate(ctx, ref, func(repos repository.TxRepositories, g *domain.Game) error {
		if g.Status != domain.GameStatusWaiting {
			return domain.ErrGameNotWaiting
		}
		if g.HasPlayer(userID) {
			return domain.ErrAlreadyJoined
		}
		if g.PlayerCount >= g.Settings.MaxPlayers {
			return domain.ErrGameFull
		}
		return repos.Games.AddPlayer(ctx, g.ID, userID)
	})
}

// Leave removes userID from a waiting game. When the host leaves, hosting
// passes to the player who joined earliest; when no players remain the game
// is cancelled.
func (s *GameService) Leave(ctx context.Context, userID uuid.UUID, ref string) error {
	_, err := s.mutate(ctx, ref, func(repos repository.TxRepositories, g *domain.Game) error {
		if !g.HasPlayer(userID) {
			return domain.ErrNotInGame
		}
		if g.Status != domain.GameStatusWaiting {
			return domain.ErrGameNotWaiting
		}
		if err := repos.Games.RemovePlayer(ctx, g.ID, userID); err != nil {
			return err
		}
		if !g.IsHost(userID) {
			return nil
		}

		// Players are ordered by join time, so the first remaining player is
		// the longest-waiting one.
		for _, p := range g.Players {
			if p.UserID != userID {
				return repos.Games.UpdateHost(ctx, g.ID, p.UserID)
			}
		}
		return repos.Games.UpdateStatus(ctx, g.ID, domain.GameStatusCancelled)
	})
	return err
}

// Start moves a waiting game to in_progress once the host asks and the game
// is ready: see checkReadyToStart.
func (s *GameService) Start(ctx context.Context, userID uuid.UUID, ref string) (*domain.Game, error) {
	return s.mutate(ctx, ref, func(repos repository.TxRepositories, g *domain.Game) error {
		if !g.IsHost(userID) {
			return domain.ErrNotGameHost
		}
		if g.Status != domain.GameStatusWaiting {
			return domain.ErrGameNotWaiting
		}
		if err := s.checkReadyToStart(ctx, repos.Challenges, g); err != nil {
			return err
		}
		return repos.Games.UpdateStatus(ctx, g.ID, domain.GameStatusInProgress)
	})
}

// Cancel ends a waiting or in-progress game. Only the host may cancel; the
// game is kept with status cancelled rather than deleted.
func (s *GameService) Cancel(ctx context.Context, userID uuid.UUID, ref string) error {
	_, err := s.mutate(ctx, ref, func(repos repository.TxRepositories, g *domain.Game) error {
		if !g.IsHost(userID) {
			return domain.ErrNotGameHost
		}
		if !g.Status.Cancellable() {
			return domain.ErrGameNotCancellable
		}
		return repos.Games.UpdateStatus(ctx, g.ID, domain.GameStatusCancelled)
	})
	return err
}

// checkReadyToStart defines when a game may start:
//   - at least domain.MinPlayers players have joined, and
//   - ChallengeFactory finds at least one challenge for the game's category
//     and difficulty. The factory repeats challenges once the pool is used up,
//     so one eligible challenge is enough for any number of rounds.
//
// The factory reads through the transaction's challenge repository because
// the game row is locked at this point; see repository.Transactor.
func (s *GameService) checkReadyToStart(ctx context.Context, challenges repository.ChallengeRepository, g *domain.Game) error {
	if g.PlayerCount < domain.MinPlayers {
		return domain.ErrNotEnoughPlayers
	}
	_, err := NewChallengeFactory(challenges, s.random).Create(ctx, ChallengeRequest{
		Category:   g.Settings.Category,
		Difficulty: g.Settings.Difficulty,
	})
	return err
}

// mutate locks the referenced game, applies fn and returns the game as it is
// afterwards, all in one transaction. The game is not reloaded when fn fails.
func (s *GameService) mutate(
	ctx context.Context,
	ref string,
	fn func(repos repository.TxRepositories, g *domain.Game) error,
) (*domain.Game, error) {
	var game *domain.Game
	err := s.tx.WithinTx(ctx, func(repos repository.TxRepositories) error {
		repo := repos.Games
		id, err := resolveGameID(ctx, repo, ref)
		if err != nil {
			return err
		}
		locked, err := repo.LockByID(ctx, id)
		if err != nil {
			return err
		}
		if err := fn(repos, locked); err != nil {
			return err
		}
		game, err = repo.GetByID(ctx, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return game, nil
}

func (s *GameService) createWithUniqueRoomCode(ctx context.Context, repo repository.GameRepository, hostID uuid.UUID) (*domain.Game, error) {
	for range maxRoomCodeAttempts {
		code := fmt.Sprintf("%0*d", domain.RoomCodeLength, s.random.IntN(roomCodeSpace))
		game, err := repo.Create(ctx, hostID, code)
		if errors.Is(err, domain.ErrRoomCodeTaken) {
			continue
		}
		return game, err
	}
	return nil, fmt.Errorf("no free room code after %d attempts: %w", maxRoomCodeAttempts, domain.ErrRoomCodeTaken)
}

// resolveGameID turns a game reference, either a room code or a UUID, into
// the game's ID. Anything else cannot name a game and is reported as not
// found.
func resolveGameID(ctx context.Context, repo repository.GameRepository, ref string) (uuid.UUID, error) {
	if domain.IsRoomCode(ref) {
		return repo.GetActiveIDByRoomCode(ctx, ref)
	}
	id, err := uuid.Parse(ref)
	if err != nil {
		return uuid.Nil, domain.ErrNotFound
	}
	return id, nil
}

func parseGameSettings(in GameSettingsInput) (domain.GameSettings, error) {
	settings := domain.GameSettings{
		Rounds:              in.Rounds,
		TimePerRoundSeconds: in.TimePerRound,
		Difficulty:          domain.Difficulty(strings.ToLower(strings.TrimSpace(in.Difficulty))),
		Category:            domain.Category(strings.ToLower(strings.TrimSpace(in.Category))),
		AIModel:             domain.AIModel(strings.ToLower(strings.TrimSpace(in.AIModel))),
		MaxPlayers:          in.MaxPlayers,
	}
	fields := make(map[string]string)

	if !slices.Contains(domain.AllowedRounds, settings.Rounds) {
		fields["rounds"] = "must be one of: " + joinInts(domain.AllowedRounds)
	}
	if !slices.Contains(domain.AllowedTimePerRoundSeconds, settings.TimePerRoundSeconds) {
		fields["time_per_round"] = "must be one of: " + joinInts(domain.AllowedTimePerRoundSeconds)
	}
	if !settings.Difficulty.Valid() {
		fields["difficulty"] = "must be one of: " + joinValues(domain.Difficulties())
	}
	if !settings.Category.Valid() {
		fields["category"] = "must be one of: " + joinValues(domain.Categories())
	}
	if !settings.AIModel.Valid() {
		fields["ai_model"] = "must be one of: " + joinValues(domain.AIModels())
	}
	if settings.MaxPlayers < domain.MinPlayers || settings.MaxPlayers > domain.MaxPlayers {
		fields["max_players"] = fmt.Sprintf("must be between %d and %d", domain.MinPlayers, domain.MaxPlayers)
	}

	if len(fields) > 0 {
		return domain.GameSettings{}, &ValidationError{Fields: fields}
	}
	return settings, nil
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}
