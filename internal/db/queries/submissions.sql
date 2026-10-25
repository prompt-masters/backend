-- name: CreateSubmission :one
-- Returns no row when the player already has a submission for the round:
-- the first one stays, whichever path wrote it.
INSERT INTO submissions (game_id, round_id, player_id, test_id, prompt, output, prompt_length, test_count, source, submitted_at_ms, locked_at_ms)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
ON CONFLICT (round_id, player_id) DO NOTHING
RETURNING *;

-- name: GetSubmission :one
SELECT * FROM submissions WHERE id = $1;

-- name: GetPlayerSubmission :one
SELECT * FROM submissions WHERE round_id = $1 AND player_id = $2;

-- name: CountSubmissions :one
SELECT COUNT(*)::int FROM submissions WHERE round_id = $1;

-- name: ListSubmissions :many
SELECT * FROM submissions
WHERE round_id = $1
ORDER BY submitted_at_ms, player_id;
