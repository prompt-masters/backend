-- +goose Up
-- The game engine's state lives here so that round timing survives a server
-- restart (Redis runs without persistence). status stays the coarse lifecycle
-- used for room codes and listings; phase refines it:
--   Lobby = waiting, RoundActive/Judging/Results = in_progress,
--   GameOver = finished. A cancelled game keeps the phase it was cancelled in.
-- The total number of rounds is game_settings.rounds.
-- Round timestamps are UTC Unix milliseconds, as sent to clients.
ALTER TABLE games
    ADD COLUMN phase               VARCHAR(16) NOT NULL DEFAULT 'Lobby',
    ADD COLUMN current_round       SMALLINT    NOT NULL DEFAULT 0,
    ADD COLUMN round_id            UUID,
    ADD COLUMN round_started_at_ms BIGINT,
    ADD COLUMN round_deadline_ms   BIGINT,
    ADD CONSTRAINT games_phase_check
        CHECK (phase IN ('Lobby', 'RoundActive', 'Judging', 'Results', 'GameOver')),
    ADD CONSTRAINT games_current_round_check CHECK (current_round >= 0),
    ADD CONSTRAINT games_round_timing_check
        CHECK ((round_id IS NULL AND round_started_at_ms IS NULL AND round_deadline_ms IS NULL)
            OR (round_id IS NOT NULL AND round_deadline_ms > round_started_at_ms));

UPDATE games SET phase = 'GameOver' WHERE status = 'finished';

-- Restart recovery looks up every game with a round in play.
CREATE INDEX games_round_active_idx ON games (id) WHERE phase = 'RoundActive';

CREATE TABLE round_submissions (
    round_id        UUID     NOT NULL,
    game_id         UUID     NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    round_number    SMALLINT NOT NULL,
    user_id         UUID     NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    prompt          TEXT     NOT NULL,
    submitted_at_ms BIGINT   NOT NULL,

    CONSTRAINT round_submissions_round_id_user_id_key PRIMARY KEY (round_id, user_id),
    CONSTRAINT round_submissions_round_number_check CHECK (round_number >= 1)
);

CREATE INDEX round_submissions_game_id_idx ON round_submissions (game_id, round_number);

-- +goose Down
DROP TABLE IF EXISTS round_submissions;
DROP INDEX IF EXISTS games_round_active_idx;
ALTER TABLE games
    DROP CONSTRAINT IF EXISTS games_round_timing_check,
    DROP CONSTRAINT IF EXISTS games_current_round_check,
    DROP CONSTRAINT IF EXISTS games_phase_check,
    DROP COLUMN IF EXISTS round_deadline_ms,
    DROP COLUMN IF EXISTS round_started_at_ms,
    DROP COLUMN IF EXISTS round_id,
    DROP COLUMN IF EXISTS current_round,
    DROP COLUMN IF EXISTS phase;
