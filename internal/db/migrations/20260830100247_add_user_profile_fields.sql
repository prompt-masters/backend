-- +goose Up
ALTER TABLE users
    ADD COLUMN avatar_url TEXT,
    ADD COLUMN elo_rating INTEGER NOT NULL DEFAULT 1200;

-- +goose Down
ALTER TABLE users
    DROP COLUMN elo_rating,
    DROP COLUMN avatar_url;
