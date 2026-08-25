ALTER TABLE users
    ADD COLUMN elo_rating     INTEGER NOT NULL DEFAULT 1200,
    ADD COLUMN email_verified BOOLEAN NOT NULL DEFAULT FALSE;

-- Only the SHA-256 of a verification token is stored. The raw token exists
-- solely in the email we send, so a leak of this table cannot be turned back
-- into working verification links.
CREATE TABLE email_verification_tokens (
    token_hash  BYTEA       PRIMARY KEY,
    user_id     BIGINT      NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at  TIMESTAMPTZ NOT NULL,
    consumed_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX email_verification_tokens_user_id_idx
    ON email_verification_tokens (user_id);
