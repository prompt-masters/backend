-- name: InsertJudgingRun :one
-- Claims a round never judged before; returns no row when a run exists.
INSERT INTO judging_runs (round_id, game_id, status, attempt, started_at_ms)
VALUES ($1, $2, 'running', 1, $3)
ON CONFLICT (round_id) DO NOTHING
RETURNING attempt;

-- name: ReclaimJudgingRun :one
-- Claims a failed run, or one whose worker stopped (running since before
-- stale_before_ms); returns no row otherwise.
UPDATE judging_runs
SET status = 'running', attempt = attempt + 1, started_at_ms = sqlc.arg(now_ms),
    finished_at_ms = NULL, error_code = NULL
WHERE round_id = sqlc.arg(round_id)
  AND (status = 'failed' OR (status = 'running' AND started_at_ms < sqlc.arg(stale_before_ms)))
RETURNING attempt;

-- name: GetJudgingRun :one
SELECT * FROM judging_runs WHERE round_id = $1;

-- name: CompleteJudgingRun :execrows
-- Only the worker holding this attempt may complete it.
UPDATE judging_runs
SET status = 'completed', finished_at_ms = sqlc.arg(finished_at_ms),
    provider = sqlc.narg(provider), model = sqlc.narg(model),
    input_tokens = sqlc.narg(input_tokens), output_tokens = sqlc.narg(output_tokens), cost_usd = sqlc.narg(cost_usd)
WHERE round_id = sqlc.arg(round_id) AND status = 'running' AND attempt = sqlc.arg(attempt);

-- name: FailJudgingRun :execrows
UPDATE judging_runs
SET status = 'failed', finished_at_ms = sqlc.arg(finished_at_ms), error_code = sqlc.arg(error_code)
WHERE round_id = sqlc.arg(round_id) AND status = 'running' AND attempt = sqlc.arg(attempt);

-- name: CreateScore :exec
INSERT INTO scores (game_id, round_id, player_id, submission_id, no_submission, label,
                    accuracy, simplicity, creativity, adherence, efficiency, total, rank,
                    rationale, provider, model, created_at_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

-- name: ListScores :many
SELECT * FROM scores WHERE round_id = $1 ORDER BY rank, player_id;
