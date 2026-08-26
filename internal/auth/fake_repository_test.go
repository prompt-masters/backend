package auth

import (
	"context"
	"strings"
	"time"
)

// fakeRepository is an in-memory Repository for service tests. It enforces
// the same case-insensitive uniqueness the database indexes do, so service
// behaviour under a duplicate is exercised without needing Postgres.
type fakeRepository struct {
	users       []*User
	nextID      int64
	createErr   error // when set, CreateUser fails with this instead
	createCalls int
	lastCreated *User // the user as handed to the repository, pre-persist
}

func (f *fakeRepository) CreateUser(ctx context.Context, u *User) (*User, error) {
	f.createCalls++
	snapshot := *u
	f.lastCreated = &snapshot

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

	created := stored
	return &created, nil
}
