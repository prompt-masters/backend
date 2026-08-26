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

// Repository persists users. CreateUser must return ErrUsernameTaken or
// ErrEmailTaken when the account collides with one that already exists.
type Repository interface {
	CreateUser(ctx context.Context, u *User) (*User, error)
}

// PostgresRepository is the Postgres-backed Repository.
type PostgresRepository struct {
	db *sql.DB
}

// NewPostgresRepository returns a Repository backed by db. The users table is
// expected to exist; see migrations/000001_create_users.up.sql.
func NewPostgresRepository(db *sql.DB) *PostgresRepository {
	return &PostgresRepository{db: db}
}

// CreateUser inserts u and returns it with the identifier and timestamps the
// database generated. u is not modified.
//
// Uniqueness is decided by the table's unique indexes rather than a prior
// lookup, so concurrent registrations of the same username cannot both
// succeed. A collision comes back as ErrUsernameTaken or ErrEmailTaken. When
// an account collides on both fields the insert aborts on the first index
// Postgres checks, so only one of the two is reported.
func (r *PostgresRepository) CreateUser(ctx context.Context, u *User) (*User, error) {
	const query = `
		INSERT INTO users (username, email, password_hash)
		VALUES ($1, $2, $3)
		RETURNING id, username, email, password_hash, created_at, updated_at`

	var created User
	err := r.db.QueryRowContext(ctx, query, u.Username, u.Email, u.PasswordHash).Scan(
		&created.ID,
		&created.Username,
		&created.Email,
		&created.PasswordHash,
		&created.CreatedAt,
		&created.UpdatedAt,
	)
	if err != nil {
		if dupErr := duplicateError(err); dupErr != nil {
			return nil, dupErr
		}
		return nil, fmt.Errorf("inserting user: %w", err)
	}

	return &created, nil
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
