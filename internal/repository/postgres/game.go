package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/repository"
)

const (
	pgUniqueViolation     = "23505"
	gamePlayersPrimaryKey = "game_players_game_id_user_id_key"
)

// TxBeginner starts transactions; *pgxpool.Pool satisfies it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type GameTransactor struct {
	pool TxBeginner
}

func NewGameTransactor(pool TxBeginner) *GameTransactor {
	return &GameTransactor{pool: pool}
}

func (t *GameTransactor) WithinTx(ctx context.Context, fn func(games repository.GameRepository) error) error {
	return pgx.BeginFunc(ctx, t.pool, func(tx pgx.Tx) error {
		return fn(NewGameRepository(db.New(tx)))
	})
}

type GameRepository struct {
	queries *db.Queries
}

// NewGameRepository binds the repository to queries, which may run on a pool
// or inside a transaction.
func NewGameRepository(queries *db.Queries) *GameRepository {
	return &GameRepository{queries: queries}
}

func (r *GameRepository) Create(ctx context.Context, hostID uuid.UUID, roomCode string) (*domain.Game, error) {
	g, err := r.queries.CreateGame(ctx, db.CreateGameParams{RoomCode: roomCode, HostID: pgUUID(hostID)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.ErrRoomCodeTaken
	}
	if err != nil {
		return nil, err
	}
	return toDomainGame(g, nil, 0), nil
}

func (r *GameRepository) CreateSettings(ctx context.Context, gameID uuid.UUID, s domain.GameSettings) error {
	return r.queries.CreateGameSettings(ctx, db.CreateGameSettingsParams{
		GameID:       pgUUID(gameID),
		Rounds:       int16(s.Rounds),
		TimePerRound: int16(s.TimePerRoundSeconds),
		Difficulty:   string(s.Difficulty),
		Category:     string(s.Category),
		AiModel:      string(s.AIModel),
		MaxPlayers:   int16(s.MaxPlayers),
	})
}

func (r *GameRepository) UpdateSettings(ctx context.Context, gameID uuid.UUID, s domain.GameSettings) error {
	return r.queries.UpdateGameSettings(ctx, db.UpdateGameSettingsParams{
		GameID:       pgUUID(gameID),
		Rounds:       int16(s.Rounds),
		TimePerRound: int16(s.TimePerRoundSeconds),
		Difficulty:   string(s.Difficulty),
		Category:     string(s.Category),
		AiModel:      string(s.AIModel),
		MaxPlayers:   int16(s.MaxPlayers),
	})
}

func (r *GameRepository) GetByID(ctx context.Context, id uuid.UUID) (*domain.Game, error) {
	row, err := r.queries.GetGameByID(ctx, pgUUID(id))
	if err != nil {
		return nil, translateGetError(err)
	}
	return r.withPlayers(ctx, toDomainGame(&row.Game, &row.GameSetting, row.PlayerCount))
}

func (r *GameRepository) LockByID(ctx context.Context, id uuid.UUID) (*domain.Game, error) {
	row, err := r.queries.LockGameByID(ctx, pgUUID(id))
	if err != nil {
		return nil, translateGetError(err)
	}
	return r.withPlayers(ctx, toDomainGame(&row.Game, &row.GameSetting, row.PlayerCount))
}

func (r *GameRepository) GetActiveIDByRoomCode(ctx context.Context, roomCode string) (uuid.UUID, error) {
	id, err := r.queries.GetActiveGameIDByRoomCode(ctx, roomCode)
	if err != nil {
		return uuid.Nil, translateGetError(err)
	}
	return uuidFromPg(id), nil
}

func (r *GameRepository) ListByStatus(ctx context.Context, status domain.GameStatus, limit int) ([]*domain.Game, error) {
	rows, err := r.queries.ListGamesByStatus(ctx, db.ListGamesByStatusParams{
		Status: string(status),
		Limit:  int32(limit),
	})
	if err != nil {
		return nil, err
	}
	games := make([]*domain.Game, len(rows))
	for i, row := range rows {
		games[i] = toDomainGame(&row.Game, &row.GameSetting, row.PlayerCount)
	}
	return games, nil
}

func (r *GameRepository) AddPlayer(ctx context.Context, gameID, userID uuid.UUID) error {
	err := r.queries.AddGamePlayer(ctx, db.AddGamePlayerParams{GameID: pgUUID(gameID), UserID: pgUUID(userID)})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == gamePlayersPrimaryKey {
		return domain.ErrAlreadyJoined
	}
	return err
}

func (r *GameRepository) RemovePlayer(ctx context.Context, gameID, userID uuid.UUID) error {
	n, err := r.queries.RemoveGamePlayer(ctx, db.RemoveGamePlayerParams{GameID: pgUUID(gameID), UserID: pgUUID(userID)})
	if err != nil {
		return err
	}
	if n == 0 {
		return domain.ErrNotInGame
	}
	return nil
}

func (r *GameRepository) UpdateHost(ctx context.Context, gameID, hostID uuid.UUID) error {
	return r.queries.UpdateGameHost(ctx, db.UpdateGameHostParams{ID: pgUUID(gameID), HostID: pgUUID(hostID)})
}

func (r *GameRepository) UpdateStatus(ctx context.Context, gameID uuid.UUID, status domain.GameStatus) error {
	return r.queries.UpdateGameStatus(ctx, db.UpdateGameStatusParams{ID: pgUUID(gameID), Status: string(status)})
}

func (r *GameRepository) withPlayers(ctx context.Context, g *domain.Game) (*domain.Game, error) {
	rows, err := r.queries.ListGamePlayers(ctx, pgUUID(g.ID))
	if err != nil {
		return nil, err
	}
	g.Players = make([]domain.GamePlayer, len(rows))
	for i, row := range rows {
		g.Players[i] = domain.GamePlayer{
			UserID:   uuidFromPg(row.UserID),
			Username: row.Username,
			JoinedAt: row.JoinedAt.Time,
		}
	}
	return g, nil
}

func toDomainGame(g *db.Game, s *db.GameSetting, playerCount int32) *domain.Game {
	game := &domain.Game{
		ID:          uuidFromPg(g.ID),
		RoomCode:    g.RoomCode,
		HostID:      uuidFromPg(g.HostID),
		Status:      domain.GameStatus(g.Status),
		PlayerCount: int(playerCount),
		CreatedAt:   g.CreatedAt.Time,
		UpdatedAt:   g.UpdatedAt.Time,
	}
	if g.StartedAt.Valid {
		started := g.StartedAt.Time
		game.StartedAt = &started
	}
	if s != nil {
		game.Settings = domain.GameSettings{
			Rounds:              int(s.Rounds),
			TimePerRoundSeconds: int(s.TimePerRound),
			Difficulty:          domain.Difficulty(s.Difficulty),
			Category:            domain.Category(s.Category),
			AIModel:             domain.AIModel(s.AiModel),
			MaxPlayers:          int(s.MaxPlayers),
		}
	}
	return game
}
