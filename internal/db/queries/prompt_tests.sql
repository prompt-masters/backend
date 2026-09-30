-- name: NextPromptTestNumber :one
-- Callers hold the game row lock, which serializes allocation; the unique
-- constraint is the safety net.
SELECT (COALESCE(MAX(test_number), 0) + 1)::int
FROM prompt_tests
WHERE round_id = $1 AND player_id = $2;

-- name: CountUnfinishedPromptTests :one
SELECT COUNT(*)::int
FROM prompt_tests
WHERE round_id = $1 AND player_id = $2
  AND status IN ('pending', 'streaming')
  AND created_at_ms >= sqlc.arg(since_ms);

-- name: CreatePromptTest :one
INSERT INTO prompt_tests (game_id, round_id, player_id, test_number, prompt, status, created_at_ms)
VALUES ($1, $2, $3, $4, $5, 'pending', $6)
RETURNING id;

-- name: MarkPromptTestStreaming :execrows
UPDATE prompt_tests
SET status = 'streaming', started_at_ms = $2
WHERE id = $1 AND status = 'pending';

-- name: FinishPromptTest :execrows
UPDATE prompt_tests
SET status          = sqlc.arg(status),
    output          = sqlc.narg(output),
    error_code      = sqlc.narg(error_code),
    provider        = sqlc.narg(provider),
    model           = sqlc.narg(model),
    input_tokens    = sqlc.narg(input_tokens),
    output_tokens   = sqlc.narg(output_tokens),
    cost_usd        = sqlc.narg(cost_usd),
    latency_ms      = sqlc.narg(latency_ms),
    completed_at_ms = sqlc.arg(completed_at_ms)
WHERE id = sqlc.arg(id) AND status IN ('pending', 'streaming');

-- name: ListPromptTests :many
SELECT * FROM prompt_tests
WHERE round_id = $1 AND player_id = $2
ORDER BY test_number;

-- name: FailStalePromptTests :execrows
-- Tests left unfinished by a server that stopped mid-run.
UPDATE prompt_tests
SET status = 'failed', error_code = 'INTERRUPTED', completed_at_ms = sqlc.arg(now_ms)
WHERE status IN ('pending', 'streaming') AND created_at_ms < sqlc.arg(before_ms);
