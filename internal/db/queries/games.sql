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
-- Row-locks the game so membership and status changes are serialized. Read
-- the game in a later statement: in READ COMMITTED, anything else computed by
-- this statement (such as a player count subquery) would use the snapshot from
-- before the lock wait and miss changes committed by the previous lock holder.
SELECT id FROM games WHERE id = $1 FOR UPDATE;

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

-- name: UpdateGameProgress :execrows
-- Compare-and-set: applies only while the stored phase, status and round are
-- still the ones the change was decided from, so a duplicate or stale
-- transition updates no row, even from another server instance.
UPDATE games
SET phase               = sqlc.arg(to_phase)::varchar,
    status              = sqlc.arg(to_status)::varchar,
    current_round       = sqlc.arg(current_round)::smallint,
    round_id            = sqlc.narg(round_id)::uuid,
    round_started_at_ms = sqlc.narg(round_started_at_ms)::bigint,
    round_deadline_ms   = sqlc.narg(round_deadline_ms)::bigint,
    round_challenge_id  = sqlc.narg(round_challenge_id)::uuid,
    started_at          = CASE WHEN sqlc.arg(to_status)::varchar = 'in_progress'
                               THEN COALESCE(started_at, NOW()) ELSE started_at END,
    updated_at          = NOW()
WHERE id = sqlc.arg(id)
  AND phase = sqlc.arg(from_phase)::varchar
  AND status = sqlc.arg(from_status)::varchar
  AND round_id IS NOT DISTINCT FROM sqlc.narg(from_round_id)::uuid;

-- name: GetGameProgress :one
SELECT g.phase, g.current_round, g.round_id, g.round_started_at_ms, g.round_deadline_ms, g.round_challenge_id, s.rounds
FROM games g
JOIN game_settings s ON s.game_id = g.id
WHERE g.id = $1;

-- name: ListGamesByPhase :many
SELECT sqlc.embed(g), sqlc.embed(s)
FROM games g
JOIN game_settings s ON s.game_id = g.id
WHERE g.phase = $1
ORDER BY g.id;

-- name: CreateGameRound :exec
INSERT INTO game_rounds (id, game_id, number, started_at_ms, deadline_ms, challenge_id)
VALUES ($1, $2, $3, $4, $5, $6);

-- name: ListGameChallengeIDs :many
-- The challenges already played in a game, so a new round avoids them.
SELECT challenge_id::uuid FROM game_rounds
WHERE game_id = $1 AND challenge_id IS NOT NULL
ORDER BY number;

-- name: GetGameRound :one
SELECT * FROM game_rounds WHERE id = $1;

-- name: LockGameShared :one
-- A shared lock: it waits for, and holds off, engine transitions (which lock
-- FOR UPDATE), while other shared lockers proceed in parallel.
SELECT id FROM games WHERE id = $1 FOR SHARE;
