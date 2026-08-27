package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestRegisterGivesTheNewAccountTheDefaultEloRating(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if reg.User.EloRating != DefaultEloRating {
		t.Errorf("EloRating = %d, want %d", reg.User.EloRating, DefaultEloRating)
	}
	if DefaultEloRating != 1200 {
		t.Errorf("DefaultEloRating = %d, want 1200", DefaultEloRating)
	}
}

func TestRegisterLeavesTheAccountUnverified(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if reg.User.EmailVerified {
		t.Error("EmailVerified = true, want false until the emailed link is followed")
	}
}

func TestRegisterIssuesAUsableVerificationToken(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if reg.VerificationToken == "" {
		t.Fatal("VerificationToken is empty, want a token to put in the email")
	}
	// 32 random bytes base64url-encoded without padding is 43 characters.
	raw, err := base64.RawURLEncoding.DecodeString(reg.VerificationToken)
	if err != nil {
		t.Fatalf("token %q is not URL-safe base64: %v", reg.VerificationToken, err)
	}
	if len(raw) < 32 {
		t.Errorf("token carries %d bytes of entropy, want at least 32", len(raw))
	}
}

func TestRegisterIssuesADifferentTokenEachTime(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)

	first, err := svc.Register(context.Background(), RegisterInput{
		Username: "alice", Email: "alice@example.com", Password: "hunter2secret",
	})
	if err != nil {
		t.Fatalf("first Register() error = %v", err)
	}
	second, err := svc.Register(context.Background(), RegisterInput{
		Username: "bob", Email: "bob@example.com", Password: "hunter2secret",
	})
	if err != nil {
		t.Fatalf("second Register() error = %v", err)
	}

	if first.VerificationToken == second.VerificationToken {
		t.Error("two registrations produced the same verification token")
	}
}

func TestRegisterNeverHandsTheRawTokenToTheRepository(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if repo.lastToken == nil {
		t.Fatal("no verification token reached the repository")
	}
	if string(repo.lastToken.Hash) == reg.VerificationToken {
		t.Fatal("the raw token was handed to the repository, not its hash")
	}

	want := sha256.Sum256([]byte(reg.VerificationToken))
	if !bytes.Equal(repo.lastToken.Hash, want[:]) {
		t.Errorf("stored hash = %x, want the SHA-256 of the raw token %x", repo.lastToken.Hash, want)
	}
}

func TestRegisterExpiresTheTokenAfterTheConfiguredLifetime(t *testing.T) {
	repo := &fakeRepository{}
	before := time.Now()

	if _, err := newTestService(repo).Register(context.Background(), validInput()); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if repo.lastToken == nil {
		t.Fatal("no verification token reached the repository")
	}
	got := repo.lastToken.ExpiresAt
	earliest, latest := before.Add(VerificationTokenTTL), time.Now().Add(VerificationTokenTTL)
	if got.Before(earliest) || got.After(latest) {
		t.Errorf("ExpiresAt = %v, want within [%v, %v]", got, earliest, latest)
	}
	if VerificationTokenTTL != 24*time.Hour {
		t.Errorf("VerificationTokenTTL = %v, want 24h", VerificationTokenTTL)
	}
}

func TestVerifyEmailMarksTheAccountVerified(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	ctx := context.Background()

	reg, err := svc.Register(ctx, validInput())
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	user, err := svc.VerifyEmail(ctx, reg.VerificationToken)
	if err != nil {
		t.Fatalf("VerifyEmail() error = %v, want nil", err)
	}
	if !user.EmailVerified {
		t.Error("EmailVerified = false, want true after following the link")
	}
	if user.ID != reg.User.ID {
		t.Errorf("ID = %d, want the registered user %d", user.ID, reg.User.ID)
	}
}

func TestVerifyEmailRejectsAnUnknownToken(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	ctx := context.Background()

	if _, err := svc.Register(ctx, validInput()); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, err := svc.VerifyEmail(ctx, "not-a-real-token")

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyEmailRejectsAnEmptyTokenWithoutTouchingTheRepository(t *testing.T) {
	repo := &fakeRepository{}

	_, err := newTestService(repo).VerifyEmail(context.Background(), "")

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyEmailRefusesToReuseAToken(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	ctx := context.Background()

	reg, err := svc.Register(ctx, validInput())
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if _, err := svc.VerifyEmail(ctx, reg.VerificationToken); err != nil {
		t.Fatalf("first VerifyEmail() error = %v", err)
	}

	_, err = svc.VerifyEmail(ctx, reg.VerificationToken)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyEmailRejectsAnExpiredToken(t *testing.T) {
	repo := &fakeRepository{}
	svc := NewService(repo, bcrypt.MinCost)
	ctx := context.Background()

	reg, err := svc.Register(ctx, validInput())
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	// Age the stored token past its lifetime.
	for _, stored := range repo.tokens {
		stored.expiresAt = time.Now().Add(-time.Minute)
	}

	_, err = svc.VerifyEmail(ctx, reg.VerificationToken)

	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("VerifyEmail() error = %v, want ErrTokenExpired", err)
	}
}
