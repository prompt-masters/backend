package auth

import (
	"context"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultBcryptCost is the work factor to use outside of tests.
const DefaultBcryptCost = bcrypt.DefaultCost

// Service registers new users.
type Service struct {
	repo       Repository
	bcryptCost int
}

// NewService builds a Service. Pass DefaultBcryptCost in production; tests
// pass bcrypt.MinCost so they do not pay the production work factor.
func NewService(repo Repository, bcryptCost int) *Service {
	return &Service{repo: repo, bcryptCost: bcryptCost}
}

// Register validates the input, hashes the password with bcrypt and persists
// the account. The plaintext password is never stored, logged or returned.
//
// It returns a *ValidationError if the input is malformed, ErrUsernameTaken
// or ErrEmailTaken if the account already exists, and otherwise the user as
// persisted. Uniqueness is decided by the database's unique indexes rather
// than by a prior lookup, so two concurrent registrations cannot both win.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*User, error) {
	in = normalizeRegisterInput(in)

	if err := validateRegisterInput(in); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), s.bcryptCost)
	if err != nil {
		// Deliberately does not wrap err: bcrypt error text can echo the
		// input length, and nothing derived from the password should escape.
		return nil, fmt.Errorf("hashing password: bcrypt failed")
	}

	user, err := s.repo.CreateUser(ctx, &User{
		Username:     in.Username,
		Email:        in.Email,
		PasswordHash: string(hash),
	})
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}
	return user, nil
}
