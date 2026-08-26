package auth

import (
	"errors"
	"time"
)

// Registration failures a caller is expected to handle and report.
var (
	ErrUsernameTaken = errors.New("username already taken")
	ErrEmailTaken    = errors.New("email already registered")
)

// User is a registered account. PasswordHash never leaves the process: it is
// tagged json:"-" so it cannot be serialized into a response by accident.
type User struct {
	ID           int64     `json:"id"`
	Username     string    `json:"username"`
	Email        string    `json:"email"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}
