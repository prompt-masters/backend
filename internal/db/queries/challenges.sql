-- name: ListChallenges :many
SELECT * FROM challenges
WHERE (sqlc.narg('category')::text IS NULL OR category = sqlc.narg('category')::text)
  AND (sqlc.narg('difficulty')::text IS NULL OR difficulty = sqlc.narg('difficulty')::text)
ORDER BY slug;

-- name: GetChallengeByID :one
SELECT * FROM challenges WHERE id = $1;

-- name: UpsertChallenge :one
INSERT INTO challenges (slug, title, description, category, difficulty, constraints, judge_criteria)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (slug) DO UPDATE SET
    title          = EXCLUDED.title,
    description    = EXCLUDED.description,
    category       = EXCLUDED.category,
    difficulty     = EXCLUDED.difficulty,
    constraints    = EXCLUDED.constraints,
    judge_criteria = EXCLUDED.judge_criteria,
    updated_at     = NOW()
RETURNING *;
