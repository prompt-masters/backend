package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const loginPassword = "hunter2secret"

// registerAndVerify creates an account through the service and marks it
// verified, which is the state a real user is in when they try to log in.
func registerAndVerify(t *testing.T, svc *Service, repo *fakeRepository, username, email string) *User {
	t.Helper()

	reg, err := svc.Register(context.Background(), RegisterInput{
		Username: username, Email: email, Password: loginPassword,
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	user, err := svc.VerifyEmail(context.Background(), reg.VerificationToken)
	if err != nil {
		t.Fatalf("VerifyEmail() error = %v", err)
	}
	return user
}

func TestLoginAcceptsCorrectCredentials(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registered := registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	user, err := svc.Login(context.Background(), LoginInput{
		Email: "alice@example.com", Password: loginPassword,
	})
	if err != nil {
		t.Fatalf("Login() error = %v, want nil", err)
	}

	if user.ID != registered.ID {
		t.Errorf("ID = %d, want %d", user.ID, registered.ID)
	}
	if user.Username != "alice" {
		t.Errorf("Username = %q, want %q", user.Username, "alice")
	}
}

func TestLoginNormalizesTheEmail(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	for _, email := range []string{"ALICE@EXAMPLE.COM", "  Alice@Example.com  "} {
		if _, err := svc.Login(context.Background(), LoginInput{Email: email, Password: loginPassword}); err != nil {
			t.Errorf("Login(%q) error = %v, want nil", email, err)
		}
	}
}

func TestLoginRejectsAnUnknownEmail(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	_, err := svc.Login(context.Background(), LoginInput{
		Email: "nobody@example.com", Password: loginPassword,
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRejectsAWrongPassword(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	_, err := svc.Login(context.Background(), LoginInput{
		Email: "alice@example.com", Password: "not-the-password",
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLoginRejectsAnEmptyPassword(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	_, err := svc.Login(context.Background(), LoginInput{Email: "alice@example.com", Password: ""})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
	}
}

// An unverified account must be refused with the same error as a wrong
// password, so the response cannot be used to discover registered addresses.
func TestLoginRejectsAnUnverifiedAccountWithTheSameError(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)

	if _, err := svc.Register(context.Background(), RegisterInput{
		Username: "alice", Email: "alice@example.com", Password: loginPassword,
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	_, err := svc.Login(context.Background(), LoginInput{
		Email: "alice@example.com", Password: loginPassword,
	})

	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("Login() error = %v, want ErrInvalidCredentials for an unverified account", err)
	}
}

func TestLoginPropagatesUnexpectedRepositoryErrors(t *testing.T) {
	wantErr := errors.New("connection refused")
	repo := &fakeRepository{findErr: wantErr}

	_, err := newTestService(repo).Login(context.Background(), LoginInput{
		Email: "alice@example.com", Password: loginPassword,
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Login() error = %v, want it to wrap %v", err, wantErr)
	}
	if errors.Is(err, ErrInvalidCredentials) {
		t.Error("a database outage was reported as bad credentials")
	}
}

// TestLoginSpendsTheSameEffortOnAnUnknownEmail guards the timing oracle: if
// an unknown address returns before bcrypt runs, the response time reveals
// which addresses are registered.
func TestLoginSpendsTheSameEffortOnAnUnknownEmail(t *testing.T) {
	if testing.Short() {
		t.Skip("timing comparison needs a realistic bcrypt cost")
	}

	repo := &fakeRepository{}
	// A realistic cost; at MinCost both paths are too fast to compare.
	svc := NewService(repo, 10)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	measure := func(email string) time.Duration {
		best := time.Hour
		for i := 0; i < 3; i++ {
			start := time.Now()
			svc.Login(context.Background(), LoginInput{Email: email, Password: "wrong-password"})
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}

	known := measure("alice@example.com")
	unknown := measure("nobody@example.com")

	// Sanity check: without this a comparison of two instant paths would
	// hold vacuously and prove nothing.
	if known < 10*time.Millisecond {
		t.Fatalf("a known-email login took %v; bcrypt does not appear to run at all", known)
	}
	if unknown < known/2 {
		t.Errorf("unknown email took %v but a known one took %v; the gap leaks which accounts exist", unknown, known)
	}
}

func TestLoginResultCarriesNoPlaintextPassword(t *testing.T) {
	repo := &fakeRepository{}
	svc := newTestService(repo)
	registerAndVerify(t, svc, repo, "alice", "alice@example.com")

	user, err := svc.Login(context.Background(), LoginInput{
		Email: "alice@example.com", Password: loginPassword,
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}

	if user.PasswordHash == loginPassword {
		t.Error("PasswordHash is the plaintext password")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(loginPassword)); err != nil {
		t.Errorf("the returned hash does not verify: %v", err)
	}
}
