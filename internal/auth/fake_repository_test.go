package auth

import (
	"context"
	"encoding/hex"
	"strings"
	"time"
)

type storedToken struct {
	userID     int64
	expiresAt  time.Time
	consumedAt *time.Time
}

// fakeRepository is an in-memory Repository for service tests. It enforces
// the same case-insensitive uniqueness and token rules the database does, so
// service behaviour is exercised without needing Postgres.
type fakeRepository struct {
	users  []*User
	tokens map[string]*storedToken // keyed by hex(token hash)
	nextID int64

	createErr   error // when set, CreateUserWithVerificationToken fails with this
	createCalls int
	lastCreated *User              // the user as handed to the repository
	lastToken   *VerificationToken // the token as handed to the repository

	findErr   error // when set, FindUserByEmail fails with this
	findCalls int
}

func (f *fakeRepository) CreateUserWithVerificationToken(ctx context.Context, u *User, t *VerificationToken) (*User, error) {
	f.createCalls++
	userSnapshot, tokenSnapshot := *u, *t
	f.lastCreated, f.lastToken = &userSnapshot, &tokenSnapshot

	if f.createErr != nil {
		return nil, f.createErr
	}

	for _, existing := range f.users {
		if strings.EqualFold(existing.Username, u.Username) {
			return nil, ErrUsernameTaken
		}
		if strings.EqualFold(existing.Email, u.Email) {
			return nil, ErrEmailTaken
		}
	}

	f.nextID++
	stored := *u
	stored.ID = f.nextID
	stored.CreatedAt = time.Now().UTC()
	stored.UpdatedAt = stored.CreatedAt
	f.users = append(f.users, &stored)

	if f.tokens == nil {
		f.tokens = make(map[string]*storedToken)
	}
	f.tokens[hex.EncodeToString(t.Hash)] = &storedToken{userID: stored.ID, expiresAt: t.ExpiresAt}

	created := stored
	return &created, nil
}

func (f *fakeRepository) VerifyEmail(ctx context.Context, tokenHash []byte) (*User, error) {
	token, ok := f.tokens[hex.EncodeToString(tokenHash)]
	if !ok || token.consumedAt != nil {
		return nil, ErrInvalidToken
	}
	if !token.expiresAt.After(time.Now()) {
		return nil, ErrTokenExpired
	}

	now := time.Now().UTC()
	token.consumedAt = &now

	for _, u := range f.users {
		if u.ID == token.userID {
			u.EmailVerified = true
			u.UpdatedAt = now
			verified := *u
			return &verified, nil
		}
	}
	return nil, ErrInvalidToken
}

func (f *fakeRepository) FindUserByEmail(ctx context.Context, email string) (*User, error) {
	f.findCalls++
	if f.findErr != nil {
		return nil, f.findErr
	}
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			found := *u
			return &found, nil
		}
	}
	return nil, ErrUserNotFound
}
