-- +goose Up
-- Each round is played on one challenge, picked as it starts. games carries
-- the current round's challenge alongside its other round fields.
ALTER TABLE game_rounds ADD COLUMN challenge_id UUID REFERENCES challenges(id);
ALTER TABLE games ADD COLUMN round_challenge_id UUID REFERENCES challenges(id);

-- Final submissions replace round_submissions: a submission is now a
-- snapshot of one of the player's completed prompt tests, and the old
-- prompt-only rows carry no output to judge.
DROP TABLE round_submissions;

CREATE TABLE submissions (
    id              UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id         UUID        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    round_id        UUID        NOT NULL REFERENCES game_rounds(id) ON DELETE CASCADE,
    player_id       UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    test_id         UUID        NOT NULL REFERENCES prompt_tests(id),
    -- Copies of the test's prompt and output at submission time.
    prompt          TEXT        NOT NULL,
    output          TEXT        NOT NULL,
    -- Unicode code points of the trimmed prompt.
    prompt_length   INT         NOT NULL,
    -- The player's prompt tests in the round when they submitted, any status.
    test_count      INT         NOT NULL,
    source          VARCHAR(8)  NOT NULL,
    submitted_at_ms BIGINT      NOT NULL,
    locked_at_ms    BIGINT      NOT NULL,

    -- At most one final submission per player and round.
    CONSTRAINT submissions_round_id_player_id_key UNIQUE (round_id, player_id),
    CONSTRAINT submissions_source_check CHECK (source IN ('manual', 'auto')),
    CONSTRAINT submissions_counts_check CHECK (prompt_length >= 0 AND test_count >= 1)
);

CREATE INDEX submissions_game_id_idx ON submissions (game_id);

-- Submissions are locked when written: the database refuses any update.
-- +goose StatementBegin
CREATE FUNCTION submissions_refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'submissions are immutable' USING ERRCODE = 'integrity_constraint_violation';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER submissions_immutable
    BEFORE UPDATE ON submissions
    FOR EACH ROW EXECUTE FUNCTION submissions_refuse_update();

-- One judging run per round. A worker judges a round only after claiming its
-- run, which is what keeps judging, and scores, from happening twice.
CREATE TABLE judging_runs (
    round_id       UUID        PRIMARY KEY REFERENCES game_rounds(id) ON DELETE CASCADE,
    game_id        UUID        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    status         VARCHAR(16) NOT NULL,
    attempt        INT         NOT NULL DEFAULT 1,
    started_at_ms  BIGINT      NOT NULL,
    finished_at_ms BIGINT,
    error_code     VARCHAR(32),
    -- The judge call that produced the scores (one call judges a round).
    provider       VARCHAR(64),
    model          VARCHAR(128),
    input_tokens   INT,
    output_tokens  INT,
    cost_usd       DOUBLE PRECISION,

    CONSTRAINT judging_runs_status_check CHECK (status IN ('running', 'completed', 'failed'))
);

CREATE INDEX judging_runs_game_id_idx ON judging_runs (game_id);

-- Scores are integer hundredths of a point on a 0-10 scale (7.60 is 760), so
-- totals compare exactly. A player with no submission gets a zero row.
CREATE TABLE scores (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id       UUID        NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    round_id      UUID        NOT NULL REFERENCES game_rounds(id) ON DELETE CASCADE,
    player_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    submission_id UUID        REFERENCES submissions(id),
    no_submission BOOLEAN     NOT NULL,
    -- The anonymous label the judge saw.
    label         VARCHAR(8),
    accuracy      INT         NOT NULL,
    simplicity    INT         NOT NULL,
    creativity    INT         NOT NULL,
    adherence     INT         NOT NULL,
    efficiency    INT         NOT NULL,
    total         INT         NOT NULL,
    rank          INT         NOT NULL,
    rationale     TEXT,
    provider      VARCHAR(64),
    model         VARCHAR(128),
    created_at_ms BIGINT      NOT NULL,

    CONSTRAINT scores_round_id_player_id_key UNIQUE (round_id, player_id),
    CONSTRAINT scores_submission_id_key UNIQUE (submission_id),
    CONSTRAINT scores_submission_check CHECK (no_submission = (submission_id IS NULL)),
    CONSTRAINT scores_rank_check CHECK (rank >= 1)
);

CREATE INDEX scores_game_id_idx ON scores (game_id);

-- +goose Down
DROP TABLE IF EXISTS scores;
DROP TABLE IF EXISTS judging_runs;
DROP TABLE IF EXISTS submissions;
DROP FUNCTION IF EXISTS submissions_refuse_update();
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
ALTER TABLE games DROP COLUMN IF EXISTS round_challenge_id;
ALTER TABLE game_rounds DROP COLUMN IF EXISTS challenge_id;
