-- +goose Up
-- Each player's helper points for one game: set when the game starts and
-- shared by all its rounds. The CHECK is the last guard against overspending.
CREATE TABLE helper_balances (
    game_id       UUID   NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    player_id     UUID   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    balance       INT    NOT NULL,
    updated_at_ms BIGINT NOT NULL,

    CONSTRAINT helper_balances_pkey PRIMARY KEY (game_id, player_id),
    CONSTRAINT helper_balances_balance_check CHECK (balance >= 0)
);

-- Every paid helper request: reserved when the points are taken, then
-- delivered or refunded. Refunded rows are kept as an audit trail.
-- Timestamps are UTC Unix milliseconds.
CREATE TABLE helper_usage (
    id              UUID             PRIMARY KEY DEFAULT gen_random_uuid(),
    game_id         UUID             NOT NULL REFERENCES games(id) ON DELETE CASCADE,
    round_id        UUID             NOT NULL REFERENCES game_rounds(id) ON DELETE CASCADE,
    player_id       UUID             NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    helper_type     VARCHAR(16)      NOT NULL,
    -- The price when it was bought.
    cost            INT              NOT NULL,
    status          VARCHAR(16)      NOT NULL DEFAULT 'reserved',
    idempotency_key VARCHAR(128)     NOT NULL,
    input_prompt    TEXT,
    test_id         UUID             REFERENCES prompt_tests(id),
    content         TEXT,
    error_code      VARCHAR(32),
    provider        VARCHAR(64),
    model           VARCHAR(128),
    input_tokens    INT,
    output_tokens   INT,
    cost_usd        DOUBLE PRECISION,
    created_at_ms   BIGINT           NOT NULL,
    delivered_at_ms BIGINT,
    refunded_at_ms  BIGINT,

    -- A retried request (same key) is never charged twice.
    CONSTRAINT helper_usage_player_id_idempotency_key_key UNIQUE (player_id, idempotency_key),
    CONSTRAINT helper_usage_type_check CHECK (helper_type IN ('hint', 'suggestion', 'review')),
    CONSTRAINT helper_usage_status_check CHECK (status IN ('reserved', 'delivered', 'refunded')),
    CONSTRAINT helper_usage_cost_check CHECK (cost >= 0)
);

CREATE INDEX helper_usage_round_player_idx ON helper_usage (round_id, player_id, created_at_ms);
-- Finds reservations left behind, for refunding.
CREATE INDEX helper_usage_reserved_idx ON helper_usage (created_at_ms) WHERE status = 'reserved';

-- +goose Down
DROP TABLE IF EXISTS helper_usage;
DROP TABLE IF EXISTS helper_balances;
