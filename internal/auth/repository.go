package auth

import "context"

// Repository persists users. CreateUser must return ErrUsernameTaken or
// ErrEmailTaken when the account collides with one that already exists.
type Repository interface {
	CreateUser(ctx context.Context, u *User) (*User, error)
}
