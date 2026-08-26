DROP TABLE email_verification_tokens;

ALTER TABLE users
    DROP COLUMN email_verified,
    DROP COLUMN elo_rating;
