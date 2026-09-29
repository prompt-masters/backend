-- name: CreateRoundSubmission :exec
INSERT INTO round_submissions (round_id, game_id, round_number, user_id, prompt, submitted_at_ms)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: CountRoundSubmissions :one
SELECT COUNT(*)::int FROM round_submissions WHERE round_id = $1;
