-- name: CreateRefreshToken :exec
INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
VALUES ($1, $2, $3);

-- name: GetActiveRefreshTokenUserID :one
SELECT user_id
FROM refresh_tokens
WHERE token_hash = $1
  AND revoked_at IS NULL
  AND expires_at > NOW();

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens
SET revoked_at = NOW()
WHERE token_hash = $1 AND revoked_at IS NULL;

-- name: RotateRefreshToken :one
WITH revoked AS (
    UPDATE refresh_tokens
    SET revoked_at = NOW()
    WHERE refresh_tokens.token_hash = $1
      AND refresh_tokens.revoked_at IS NULL
      AND refresh_tokens.expires_at > NOW()
    RETURNING refresh_tokens.user_id
)
INSERT INTO refresh_tokens (token_hash, user_id, expires_at)
SELECT $2, user_id, $3
FROM revoked
RETURNING user_id;
