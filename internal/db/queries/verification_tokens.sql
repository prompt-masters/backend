-- name: CreateVerificationToken :one
INSERT INTO verification_tokens (user_id, token, expires_at)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetVerificationToken :one
SELECT * FROM verification_tokens WHERE token = $1;

-- name: DeleteVerificationToken :exec
DELETE FROM verification_tokens WHERE token = $1;

-- name: DeleteExpiredTokens :exec
DELETE FROM verification_tokens WHERE expires_at < NOW();
