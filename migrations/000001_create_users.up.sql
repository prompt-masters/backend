CREATE TABLE users (
    id            BIGSERIAL PRIMARY KEY,
    username      VARCHAR(32)  NOT NULL,
    email         TEXT         NOT NULL,
    password_hash TEXT         NOT NULL,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now()
);

-- Usernames keep their original casing for display, but must be unique
-- case-insensitively so "Alice" cannot be registered alongside "alice".
CREATE UNIQUE INDEX users_username_lower_key ON users (LOWER(username));

-- Emails are normalized to lowercase before insert, so a plain unique
-- index is enough to make them case-insensitively unique.
CREATE UNIQUE INDEX users_email_key ON users (email);
