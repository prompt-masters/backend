-- +goose Up
-- One row per round ever played, written in the transaction that starts it,
-- so a round keeps resolving to its game after the game moves on.
CREATE TABLE game_rounds (
    id            UUID     PRIMARY KEY,
    game_id       UUID     NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    number        SMALLINT NOT NULL,
    started_at_ms BIGINT   NOT NULL,
    deadline_ms   BIGINT   NOT NULL,

    CONSTRAINT game_rounds_game_id_number_key UNIQUE (game_id, number),
    CONSTRAINT game_rounds_number_check CHECK (number >= 1),
    CONSTRAINT game_rounds_timing_check CHECK (deadline_ms > started_at_ms)
);

-- Rounds started before this migration.
INSERT INTO game_rounds (id, game_id, number, started_at_ms, deadline_ms)
SELECT round_id, id, current_round, round_started_at_ms, round_deadline_ms
FROM games
WHERE round_id IS NOT NULL;

-- Every prompt test a player runs, kept for efficiency scoring and replay.
-- Timestamps are UTC Unix milliseconds.
CREATE TABLE prompt_tests (
    id              UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id         UUID             NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    round_id        UUID             NOT NULL REFERENCES game_rounds(id) ON DELETE CASCADE,
    player_id       UUID             NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    test_number     INT              NOT NULL,
    prompt          TEXT             NOT NULL,
    output          TEXT,
    status          VARCHAR(16)      NOT NULL DEFAULT 'pending',
    error_code      VARCHAR(32),
    provider        VARCHAR(64),
    model           VARCHAR(128),
    input_tokens    INT,
    output_tokens   INT,
    cost_usd        DOUBLE PRECISION,
    latency_ms      BIGINT,
    created_at_ms   BIGINT           NOT NULL,
    started_at_ms   BIGINT,
    completed_at_ms BIGINT,

    -- Also the index for a player's ordered history in a round.
    CONSTRAINT prompt_tests_round_id_player_id_test_number_key UNIQUE (round_id, player_id, test_number),
    CONSTRAINT prompt_tests_test_number_check CHECK (test_number >= 1),
    CONSTRAINT prompt_tests_status_check
        CHECK (status IN ('pending', 'streaming', 'completed', 'failed', 'canceled'))
);

CREATE INDEX prompt_tests_game_id_idx ON prompt_tests (game_id);
CREATE INDEX prompt_tests_player_id_idx ON prompt_tests (player_id);
-- Finds unfinished tests for the concurrency limit and start-up cleanup.
CREATE INDEX prompt_tests_unfinished_idx ON prompt_tests (created_at_ms)
    WHERE status IN ('pending', 'streaming');

-- +goose Down
DROP TABLE IF EXISTS prompt_tests;
DROP TABLE IF EXISTS game_rounds;
