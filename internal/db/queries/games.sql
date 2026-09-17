-- name: CreateGame :one
-- Returns no row when the room code is held by another active game.
INSERT INTO games (room_code, host_id)
VALUES ($1, $2)
ON CONFLICT (room_code) WHERE status IN ('waiting', 'in_progress') DO NOTHING
RETURNING *;

-- name: CreateGameSettings :exec
INSERT INTO game_settings (game_id, rounds, time_per_round, difficulty, category, ai_model, max_players)
VALUES ($1, $2, $3, $4, $5, $6, $7);

-- name: UpdateGameSettings :exec
UPDATE game_settings
SET rounds         = $2,
    time_per_round = $3,
    difficulty     = $4,
    category       = $5,
    ai_model       = $6,
    max_players    = $7,
    updated_at     = NOW()
WHERE game_id = $1;

-- name: GetGameByID :one
SELECT sqlc.embed(g), sqlc.embed(s),
       (SELECT COUNT(*) FROM game_players p WHERE p.game_id = g.id)::int AS player_count
FROM games g
JOIN game_settings s ON s.game_id = g.id
WHERE g.id = $1;

-- name: LockGameByID :one
-- Row-locks the game so membership and status changes are serialized.
SELECT sqlc.embed(g), sqlc.embed(s),
       (SELECT COUNT(*) FROM game_players p WHERE p.game_id = g.id)::int AS player_count
FROM games g
JOIN game_settings s ON s.game_id = g.id
WHERE g.id = $1
FOR UPDATE OF g;

-- name: GetActiveGameIDByRoomCode :one
SELECT id FROM games
WHERE room_code = $1 AND status IN ('waiting', 'in_progress');

-- name: ListGamesByStatus :many
SELECT sqlc.embed(g), sqlc.embed(s),
       (SELECT COUNT(*) FROM game_players p WHERE p.game_id = g.id)::int AS player_count
FROM games g
JOIN game_settings s ON s.game_id = g.id
WHERE g.status = $1
ORDER BY g.created_at DESC, g.id
LIMIT $2;

-- name: ListGamePlayers :many
SELECT p.user_id, u.username, p.joined_at
FROM game_players p
JOIN users u ON u.id = p.user_id
WHERE p.game_id = $1
ORDER BY p.joined_at, p.user_id;

-- name: AddGamePlayer :exec
INSERT INTO game_players (game_id, user_id) VALUES ($1, $2);

-- name: RemoveGamePlayer :execrows
DELETE FROM game_players WHERE game_id = $1 AND user_id = $2;

-- name: UpdateGameHost :exec
UPDATE games SET host_id = $2, updated_at = NOW() WHERE id = $1;

-- name: UpdateGameStatus :exec
UPDATE games
SET status     = sqlc.arg(status)::varchar,
    started_at = CASE WHEN sqlc.arg(status)::varchar = 'in_progress' THEN NOW() ELSE started_at END,
    updated_at = NOW()
WHERE id = sqlc.arg(id);
