package auth

import (
	"context"
	"errors"
	"testing"
)

func TestFindUserByEmailReturnsTheAccount(t *testing.T) {
	repo := newTestRepository(t)
	created, _ := registerFixture(t, repo)

	found, err := repo.FindUserByEmail(context.Background(), "alice@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail() error = %v, want nil", err)
	}

	if found.ID != created.ID {
		t.Errorf("ID = %d, want %d", found.ID, created.ID)
	}
	if found.Username != "alice" {
		t.Errorf("Username = %q, want %q", found.Username, "alice")
	}
	if found.PasswordHash != "hash" {
		t.Errorf("PasswordHash = %q, want it loaded so the password can be checked", found.PasswordHash)
	}
	if found.EloRating != DefaultEloRating {
		t.Errorf("EloRating = %d, want %d", found.EloRating, DefaultEloRating)
	}
	if found.EmailVerified {
		t.Error("EmailVerified = true, want false for a freshly registered account")
	}
}

func TestFindUserByEmailReportsAMiss(t *testing.T) {
	repo := newTestRepository(t)
	registerFixture(t, repo)

	_, err := repo.FindUserByEmail(context.Background(), "nobody@example.com")

	if !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("FindUserByEmail() error = %v, want ErrUserNotFound", err)
	}
}

func TestFindUserByEmailSeesTheVerifiedFlag(t *testing.T) {
	repo := newTestRepository(t)
	_, rawToken := registerFixture(t, repo)
	ctx := context.Background()

	if _, err := repo.VerifyEmail(ctx, hashVerificationToken(rawToken)); err != nil {
		t.Fatalf("VerifyEmail() error = %v", err)
	}

	found, err := repo.FindUserByEmail(ctx, "alice@example.com")
	if err != nil {
		t.Fatalf("FindUserByEmail() error = %v", err)
	}
	if !found.EmailVerified {
		t.Error("EmailVerified = false, want true after verification")
	}
}
