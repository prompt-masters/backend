package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lib/pq"
)

// Names of the unique indexes created in migration 000001. Postgres reports
// the violated index in the error, which is what lets a single failed INSERT
// say which field collided.
const (
	usernameUniqueIndex = "users_username_lower_key"
	emailUniqueIndex    = "users_email_key"

	// pgUniqueViolation is SQLSTATE 23505.
	pgUniqueViolation = "23505"
)

// Repository persists users and their email verification tokens.
type Repository interface {
	// CreateUserWithVerificationToken stores the user and token together, or
	// neither. It returns ErrUsernameTaken or ErrEmailTaken on a collision.
	CreateUserWithVerificationToken(ctx context.Context, u *User, t *VerificationToken) (*User, error)

	// VerifyEmail spends the token with the given hash and marks its user
	// verified, returning ErrInvalidToken or ErrTokenExpired if it cannot.
	VerifyEmail(ctx context.Context, tokenHash []byte) (*User, error)
}

// PostgresRepository is the Postgres-backed Repository.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository returns a Repository backed by db. The schema in
// migrations/ is expected to have been applied.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

const userColumns = "id, username, email, password_hash, elo_rating, email_verified, created_at, updated_at"

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.Email, &u.PasswordHash, &u.EloRating, &u.EmailVerified, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUserWithVerificationToken inserts u and tok in one transaction and
// returns the user with the identifier and timestamps the database generated.
// Neither argument is modified.
//
// Both rows land or neither does: a user whose token insert failed could
// never verify their address, since the raw token only ever existed in the
// email we were about to send.
//
// Uniqueness is decided by the table's unique indexes rather than a prior
// lookup, so concurrent registrations of the same username cannot both
// succeed. A collision comes back as ErrUsernameTaken or ErrEmailTaken. When
// an account collides on both fields the insert aborts on the first index
// Postgres checks, so only one of the two is reported.
func (r *PostgresRepository) CreateUserWithVerificationToken(ctx context.Context, u *User, tok *VerificationToken) (*User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	const insertUser = `
		INSERT INTO users (username, email, password_hash, elo_rating)
		VALUES ($1, $2, $3, $4)
		RETURNING ` + userColumns

	created, err := scanUser(tx.QueryRowContext(ctx, insertUser, u.Username, u.Email, u.PasswordHash, u.EloRating))
	if err != nil {
		if dupErr := duplicateError(err); dupErr != nil {
			return nil, dupErr
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}

	const insertToken = `
		INSERT INTO email_verification_tokens (token_hash, user_id, expires_at)
		VALUES ($1, $2, $3)`

	if _, err := tx.ExecContext(ctx, insertToken, tok.Hash, created.ID, tok.ExpiresAt); err != nil {
		return nil, fmt.Errorf("inserting verification token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}
	return created, nil
}

// VerifyEmail spends the token with the given hash and marks its user
// verified, returning the updated user.
//
// The token row is locked for the duration so two concurrent clicks on the
// same link cannot both spend it. Expiry is judged by the database clock
// rather than the server's, so a skewed application host cannot widen or
// narrow a token's lifetime.
//
// An unknown, already-spent token is reported as ErrInvalidToken — the same
// error as one that never existed, so a caller cannot use the response to
// learn whether a token was ever issued.
func (r *PostgresRepository) VerifyEmail(ctx context.Context, tokenHash []byte) (*User, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once the tx is committed

	const selectToken = `
		SELECT user_id, consumed_at IS NOT NULL, expires_at <= now()
		FROM email_verification_tokens
		WHERE token_hash = $1
		FOR UPDATE`

	var userID int64
	var consumed, expired bool
	switch err := tx.QueryRowContext(ctx, selectToken, tokenHash).Scan(&userID, &consumed, &expired); {
	case errors.Is(err, sql.ErrNoRows):
		return nil, ErrInvalidToken
	case err != nil:
		return nil, fmt.Errorf("loading verification token: %w", err)
	case consumed:
		return nil, ErrInvalidToken
	case expired:
		return nil, ErrTokenExpired
	}

	const spendToken = `UPDATE email_verification_tokens SET consumed_at = now() WHERE token_hash = $1`
	if _, err := tx.ExecContext(ctx, spendToken, tokenHash); err != nil {
		return nil, fmt.Errorf("spending verification token: %w", err)
	}

	const markVerified = `
		UPDATE users SET email_verified = TRUE, updated_at = now()
		WHERE id = $1
		RETURNING ` + userColumns

	user, err := scanUser(tx.QueryRowContext(ctx, markVerified, userID))
	if err != nil {
		return nil, fmt.Errorf("marking email verified: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing transaction: %w", err)
	}
	return user, nil
}

// duplicateError maps a unique-index violation to the matching domain error,
// or returns nil if err is not one.
func duplicateError(err error) error {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != pgUniqueViolation {
		return nil
	}

	switch pqErr.Constraint {
	case usernameUniqueIndex:
		return ErrUsernameTaken
	case emailUniqueIndex:
		return ErrEmailTaken
	default:
		return nil
	}
}
