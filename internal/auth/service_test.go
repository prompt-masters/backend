package auth

import (
	"context"
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

func newTestService(repo Repository) *Service {
	// bcrypt.MinCost keeps the suite fast; the cost is a parameter precisely
	// so tests need not pay the production work factor.
	return NewService(repo, bcrypt.MinCost)
}

func validInput() RegisterInput {
	return RegisterInput{
		Username: "alice",
		Email:    "alice@example.com",
		Password: "hunter2secret",
	}
}

func TestRegisterStoresABcryptHashAndNeverThePlaintext(t *testing.T) {
	repo := &fakeRepository{}
	in := validInput()

	reg, err := newTestService(repo).Register(context.Background(), in)
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	user := reg.User

	if user.PasswordHash == in.Password {
		t.Fatal("PasswordHash is the plaintext password")
	}
	if strings.Contains(user.PasswordHash, in.Password) {
		t.Fatalf("PasswordHash %q contains the plaintext password", user.PasswordHash)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)); err != nil {
		t.Fatalf("stored hash does not verify against the password: %v", err)
	}
}

func TestRegisterSaltsEachHashIndependently(t *testing.T) {
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

	if first.User.PasswordHash == second.User.PasswordHash {
		t.Error("identical passwords produced identical hashes; the salt is not random")
	}
}

func TestRegisterNormalizesBeforePersisting(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), RegisterInput{
		Username: "  Alice  ",
		Email:    "  Alice@EXAMPLE.com  ",
		Password: "hunter2secret",
	})
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	user := reg.User

	if user.Username != "Alice" {
		t.Errorf("Username = %q, want %q (trimmed, casing preserved)", user.Username, "Alice")
	}
	if user.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q (trimmed and lowercased)", user.Email, "alice@example.com")
	}
}

func TestRegisterRejectsInvalidInputWithoutTouchingTheRepository(t *testing.T) {
	repo := &fakeRepository{}

	_, err := newTestService(repo).Register(context.Background(), RegisterInput{
		Username: "", Email: "nope", Password: "short",
	})

	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("Register() error = %v, want a *ValidationError", err)
	}
	if repo.createCalls != 0 {
		t.Errorf("repository was called %d times; invalid input must not reach it", repo.createCalls)
	}
}

func TestRegisterReportsADuplicateUsername(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	ctx := context.Background()

	if _, err := svc.Register(ctx, validInput()); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	// Same username in a different case, different email.
	_, err := svc.Register(ctx, RegisterInput{
		Username: "ALICE", Email: "other@example.com", Password: "hunter2secret",
	})

	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("Register() error = %v, want ErrUsernameTaken", err)
	}
}

func TestRegisterReportsADuplicateEmail(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	ctx := context.Background()

	if _, err := svc.Register(ctx, validInput()); err != nil {
		t.Fatalf("first Register() error = %v", err)
	}

	// Same email in a different case, different username.
	_, err := svc.Register(ctx, RegisterInput{
		Username: "bob", Email: "ALICE@example.com", Password: "hunter2secret",
	})

	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("Register() error = %v, want ErrEmailTaken", err)
	}
}

func TestRegisterPropagatesUnexpectedRepositoryErrors(t *testing.T) {
	wantErr := errors.New("connection refused")
	repo := &fakeRepository{createErr: wantErr}

	_, err := newTestService(repo).Register(context.Background(), validInput())

	if !errors.Is(err, wantErr) {
		t.Fatalf("Register() error = %v, want it to wrap %v", err, wantErr)
	}
}

func TestRegisterReturnsTheUserAsPersisted(t *testing.T) {
	repo := &fakeRepository{}

	reg, err := newTestService(repo).Register(context.Background(), validInput())
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}
	user := reg.User

	if user.ID == 0 {
		t.Error("ID = 0, want the identifier assigned by the repository")
	}
	if user.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the timestamp assigned by the repository")
	}
	if user.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero, want the timestamp assigned by the repository")
	}
}

func TestRegisterHandsTheRepositoryAHashedPasswordOnly(t *testing.T) {
	repo := &fakeRepository{}
	in := validInput()

	if _, err := newTestService(repo).Register(context.Background(), in); err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	if repo.lastCreated == nil {
		t.Fatal("repository was never called")
	}
	if repo.lastCreated.PasswordHash == in.Password {
		t.Error("the plaintext password was handed to the repository")
	}
	if repo.lastCreated.ID != 0 {
		t.Errorf("ID = %d, want 0; the repository assigns identifiers", repo.lastCreated.ID)
	}
}
