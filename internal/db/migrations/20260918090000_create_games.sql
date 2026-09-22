-- +goose Up
CREATE TABLE games (
    id         UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    room_code  CHAR(6)     NOT NULL,
    host_id    UUID        NOT NULL REFERENCES users(id),
    status     VARCHAR(16) NOT NULL DEFAULT 'waiting',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    started_at TIMESTAMPTZ,

    CONSTRAINT games_room_code_format_check CHECK (room_code ~ '^[0-9]{6}$'),
    CONSTRAINT games_status_check
        CHECK (status IN ('waiting', 'in_progress', 'finished', 'cancelled'))
);

-- A room code identifies one active game at a time; codes of finished or
-- cancelled games are free to be reused.
CREATE UNIQUE INDEX games_active_room_code_key
    ON games (room_code)
    WHERE status IN ('waiting', 'in_progress');

CREATE INDEX games_status_created_at_idx ON games (status, created_at DESC);
CREATE INDEX games_host_id_idx ON games (host_id);

CREATE TABLE game_settings (
    game_id        UUID        PRIMARY KEY REFERENCES games(id) ON DELETE CASCADE,
    rounds         SMALLINT    NOT NULL,
    time_per_round SMALLINT    NOT NULL,
    difficulty     VARCHAR(16) NOT NULL,
    category       VARCHAR(32) NOT NULL,
    ai_model       VARCHAR(64) NOT NULL,
    max_players    SMALLINT    NOT NULL,
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT game_settings_rounds_check CHECK (rounds IN (3, 5, 7)),
    CONSTRAINT game_settings_time_per_round_check CHECK (time_per_round IN (60, 90, 120)),
    CONSTRAINT game_settings_difficulty_check CHECK (difficulty IN ('easy', 'medium', 'hard')),
    CONSTRAINT game_settings_category_check
        CHECK (category IN ('creative_writing', 'coding', 'data_extraction', 'summarization', 'reasoning')),
    CONSTRAINT game_settings_ai_model_check
        CHECK (ai_model IN ('claude-opus-5', 'claude-sonnet-5', 'claude-haiku-4-5')),
    CONSTRAINT game_settings_max_players_check CHECK (max_players BETWEEN 2 AND 6)
);

CREATE TABLE game_players (
    game_id   UUID        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    user_id   UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    joined_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT game_players_game_id_user_id_key PRIMARY KEY (game_id, user_id)
);

CREATE INDEX game_players_user_id_idx ON game_players (user_id);

-- +goose Down
DROP TABLE IF EXISTS game_players;
DROP TABLE IF EXISTS game_settings;
DROP TABLE IF EXISTS games;
