-- name: EnsureHelperBalance :exec
INSERT INTO helper_balances (game_id, player_id, balance, updated_at_ms)
VALUES ($1, $2, $3, $4)
ON CONFLICT (game_id, player_id) DO NOTHING;

-- name: GetHelperBalance :one
SELECT balance FROM helper_balances WHERE game_id = $1 AND player_id = $2;

-- name: DebitHelperBalance :one
-- Takes cost only if the balance covers it; returns no row otherwise.
UPDATE helper_balances
SET balance = balance - sqlc.arg(cost)::int, updated_at_ms = sqlc.arg(now_ms)
WHERE game_id = sqlc.arg(game_id) AND player_id = sqlc.arg(player_id) AND balance >= sqlc.arg(cost)::int
RETURNING balance;

-- name: CreditHelperBalance :one
UPDATE helper_balances
SET balance = balance + sqlc.arg(amount)::int, updated_at_ms = sqlc.arg(now_ms)
WHERE game_id = sqlc.arg(game_id) AND player_id = sqlc.arg(player_id)
RETURNING balance;

-- name: CreateHelperUsage :one
-- Returns no row when the player already used this idempotency key.
INSERT INTO helper_usage (game_id, round_id, player_id, helper_type, cost, idempotency_key, input_prompt, test_id, created_at_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (player_id, idempotency_key) DO NOTHING
RETURNING id;

-- name: GetHelperUsage :one
SELECT * FROM helper_usage WHERE id = $1;

-- name: GetHelperUsageByKey :one
SELECT * FROM helper_usage WHERE player_id = $1 AND idempotency_key = $2;

-- name: DeliverHelperUsage :execrows
UPDATE helper_usage
SET status = 'delivered', content = sqlc.arg(content), delivered_at_ms = sqlc.arg(delivered_at_ms),
    provider = sqlc.narg(provider), model = sqlc.narg(model),
    input_tokens = sqlc.narg(input_tokens), output_tokens = sqlc.narg(output_tokens), cost_usd = sqlc.narg(cost_usd)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: RefundHelperUsage :execrows
-- Only a reservation can be refunded, so a refund happens at most once.
UPDATE helper_usage
SET status = 'refunded', error_code = sqlc.arg(error_code), refunded_at_ms = sqlc.arg(refunded_at_ms)
WHERE id = sqlc.arg(id) AND status = 'pending';

-- name: ListHelperUsage :many
SELECT * FROM helper_usage
WHERE round_id = $1 AND player_id = $2
ORDER BY created_at_ms, id;

-- name: ListStaleHelperReservations :many
SELECT * FROM helper_usage
WHERE status = 'pending' AND created_at_ms < $1
ORDER BY created_at_ms;
