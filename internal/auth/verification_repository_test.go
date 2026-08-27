package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"
)

// registerFixture creates a user through the repository and returns the raw
// token whose hash was stored alongside it.
func registerFixture(t *testing.T, repo *PostgresRepository) (*User, string) {
	t.Helper()

	rawToken, err := generateVerificationToken()
	if err != nil {
		t.Fatalf("generating token: %v", err)
	}

	user, err := repo.CreateUserWithVerificationToken(context.Background(),
		&User{Username: "alice", Email: "alice@example.com", PasswordHash: "hash", EloRating: DefaultEloRating},
		&VerificationToken{Hash: hashVerificationToken(rawToken), ExpiresAt: time.Now().Add(VerificationTokenTTL)},
	)
	if err != nil {
		t.Fatalf("CreateUserWithVerificationToken() error = %v", err)
	}
	return user, rawToken
}

func TestCreateUserWithVerificationTokenStoresOnlyTheTokenHash(t *testing.T) {
	repo := newTestRepository(t)
	user, rawToken := registerFixture(t, repo)

	var storedHash []byte
	var expiresAt time.Time
	var consumedAt *time.Time
	err := repo.db.QueryRow(
		"SELECT token_hash, expires_at, consumed_at FROM email_verification_tokens WHERE user_id = $1", user.ID,
	).Scan(&storedHash, &expiresAt, &consumedAt)
	if err != nil {
		t.Fatalf("reading the token row: %v", err)
	}

	if bytes.Equal(storedHash, []byte(rawToken)) {
		t.Fatal("the raw token was written to the database")
	}
	want := sha256.Sum256([]byte(rawToken))
	if !bytes.Equal(storedHash, want[:]) {
		t.Errorf("token_hash = %x, want the SHA-256 of the raw token %x", storedHash, want)
	}
	if consumedAt != nil {
		t.Errorf("consumed_at = %v, want NULL for a fresh token", consumedAt)
	}
	if !expiresAt.After(time.Now()) {
		t.Errorf("expires_at = %v, want a future time", expiresAt)
	}
}

func TestCreateUserWithVerificationTokenRollsBackTheUserIfTheTokenCannotBeStored(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	shared := testToken()
	if _, err := repo.CreateUserWithVerificationToken(ctx,
		&User{Username: "alice", Email: "alice@example.com", PasswordHash: "hash", EloRating: DefaultEloRating},
		shared,
	); err != nil {
		t.Fatalf("first CreateUserWithVerificationToken() error = %v", err)
	}

	// Reusing the token hash violates the primary key, so the token insert
	// fails after the user insert has already succeeded.
	_, err := repo.CreateUserWithVerificationToken(ctx,
		&User{Username: "bob", Email: "bob@example.com", PasswordHash: "hash", EloRating: DefaultEloRating},
		shared,
	)
	if err == nil {
		t.Fatal("CreateUserWithVerificationToken() error = nil, want a failure on the duplicate token")
	}

	var bobs int
	if err := repo.db.QueryRow("SELECT count(*) FROM users WHERE username = 'bob'").Scan(&bobs); err != nil {
		t.Fatalf("counting users: %v", err)
	}
	if bobs != 0 {
		t.Errorf("found %d rows for bob; the user must be rolled back when the token insert fails", bobs)
	}
}

func TestVerifyEmailMarksTheUserVerifiedAndSpendsTheToken(t *testing.T) {
	repo := newTestRepository(t)
	user, rawToken := registerFixture(t, repo)

	verified, err := repo.VerifyEmail(context.Background(), hashVerificationToken(rawToken))
	if err != nil {
		t.Fatalf("VerifyEmail() error = %v, want nil", err)
	}

	if !verified.EmailVerified {
		t.Error("returned user has EmailVerified = false, want true")
	}
	if verified.ID != user.ID {
		t.Errorf("ID = %d, want %d", verified.ID, user.ID)
	}

	var persisted bool
	var consumedAt *time.Time
	err = repo.db.QueryRow(`
		SELECT u.email_verified, t.consumed_at
		FROM users u JOIN email_verification_tokens t ON t.user_id = u.id
		WHERE u.id = $1`, user.ID).Scan(&persisted, &consumedAt)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if !persisted {
		t.Error("users.email_verified is still false in the database")
	}
	if consumedAt == nil {
		t.Error("consumed_at is still NULL; the token was not spent")
	}
}

func TestVerifyEmailRejectsATokenThatWasNeverIssued(t *testing.T) {
	repo := newTestRepository(t)
	registerFixture(t, repo)

	_, err := repo.VerifyEmail(context.Background(), hashVerificationToken("never-issued"))

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyEmailRejectsATokenThatWasAlreadySpent(t *testing.T) {
	repo := newTestRepository(t)
	_, rawToken := registerFixture(t, repo)
	ctx := context.Background()

	if _, err := repo.VerifyEmail(ctx, hashVerificationToken(rawToken)); err != nil {
		t.Fatalf("first VerifyEmail() error = %v", err)
	}

	_, err := repo.VerifyEmail(ctx, hashVerificationToken(rawToken))

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second VerifyEmail() error = %v, want ErrInvalidToken", err)
	}
}

func TestVerifyEmailRejectsAnExpiredTokenInPostgres(t *testing.T) {
	repo := newTestRepository(t)
	user, rawToken := registerFixture(t, repo)

	if _, err := repo.db.Exec(
		"UPDATE email_verification_tokens SET expires_at = now() - interval '1 minute' WHERE user_id = $1", user.ID,
	); err != nil {
		t.Fatalf("ageing the token: %v", err)
	}

	_, err := repo.VerifyEmail(context.Background(), hashVerificationToken(rawToken))

	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("VerifyEmail() error = %v, want ErrTokenExpired", err)
	}

	var verified bool
	if err := repo.db.QueryRow("SELECT email_verified FROM users WHERE id = $1", user.ID).Scan(&verified); err != nil {
		t.Fatalf("reading back: %v", err)
	}
	if verified {
		t.Error("an expired token verified the account")
	}
}
