-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: CreateUser :one
INSERT INTO users (id, username, email, password)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateUserEmail :exec
UPDATE users SET email = $2, updated_at = NOW() WHERE id = $1;

-- name: UpdateUserUsername :exec
UPDATE users SET username = $2, updated_at = NOW() WHERE id = $1;

-- name: SetEmailVerified :exec
UPDATE users SET email_verified = TRUE, updated_at = NOW() WHERE id = $1;
