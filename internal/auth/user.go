package auth

import (
	"errors"
	"time"
)

// DefaultEloRating is the rating every new account starts with. The users
// table carries the same value as a column default; setting it here keeps the
// rule visible in code rather than only in a migration.
const DefaultEloRating = 1200

// Registration failures a caller is expected to handle and report.
var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrEmailTaken    = errors.New("email already registered")

	// ErrInvalidToken covers a token that is unknown, malformed or already
	// spent. They are deliberately indistinguishable to a caller.
	ErrInvalidToken = errors.New("verification token is invalid")
	ErrTokenExpired = errors.New("verification token has expired")
)

// User is a registered account. PasswordHash never leaves the process: it is
// tagged json:"-" so it cannot be serialized into a response by accident.
type User struct {
	ID            int64     `json:"id"`
	Username      string    `json:"username"`
	Email         string    `json:"email"`
	PasswordHash  string    `json:"-"`
	EloRating     int       `json:"elo_rating"`
	EmailVerified bool      `json:"email_verified"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// VerificationToken is the stored half of an email verification token: the
// SHA-256 of the value that was emailed, plus its expiry. The raw value is
// never persisted.
type VerificationToken struct {
	Hash      []byte
	ExpiresAt time.Time
}

// Registration is the result of a successful Register. VerificationToken is
// the raw token to put in the email; it exists nowhere else, and is not
// recoverable from the database.
type Registration struct {
	User              *User
	VerificationToken string
}
