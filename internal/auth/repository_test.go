package auth

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "github.com/lib/pq"
	"golang.org/x/crypto/bcrypt"
)

// newTestRepository returns a repository backed by the database named in
// TEST_DATABASE_URL, with the users table emptied. The whole file skips when
// that variable is unset, so `go test ./...` stays green without Postgres.
func newTestRepository(t *testing.T) *PostgresRepository {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set; skipping repository integration tests")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		t.Fatalf("opening test database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	if err := db.Ping(); err != nil {
		t.Fatalf("connecting to test database: %v", err)
	}
	// CASCADE is required: email_verification_tokens has a foreign key to
	// users, and Postgres refuses to truncate a referenced table without it.
	if _, err := db.Exec("TRUNCATE users, email_verification_tokens RESTART IDENTITY CASCADE"); err != nil {
		t.Fatalf("truncating tables: %v", err)
	}

	return NewPostgresRepository(db)
}

func TestCreateUserPersistsTheRowAndReturnsItsGeneratedFields(t *testing.T) {
	repo := newTestRepository(t)

	created, err := repo.CreateUser(context.Background(), &User{
		Username: "Alice", Email: "alice@example.com", PasswordHash: "hash",
	})
	if err != nil {
		t.Fatalf("CreateUser() error = %v, want nil", err)
	}

	if created.ID == 0 {
		t.Error("ID = 0, want a generated identifier")
	}
	if created.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, want the database default")
	}
	if created.UpdatedAt.IsZero() {
		t.Error("UpdatedAt is zero, want the database default")
	}
	if created.Username != "Alice" {
		t.Errorf("Username = %q, want %q (original casing preserved)", created.Username, "Alice")
	}
	if created.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", created.Email, "alice@example.com")
	}
	if created.PasswordHash != "hash" {
		t.Errorf("PasswordHash = %q, want %q", created.PasswordHash, "hash")
	}
}

func TestCreateUserRejectsAUsernameTakenInAnotherCase(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if _, err := repo.CreateUser(ctx, &User{
		Username: "alice", Email: "alice@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("first CreateUser() error = %v", err)
	}

	_, err := repo.CreateUser(ctx, &User{
		Username: "ALICE", Email: "other@example.com", PasswordHash: "hash",
	})

	if !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("CreateUser() error = %v, want ErrUsernameTaken", err)
	}
}

func TestCreateUserRejectsADuplicateEmail(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if _, err := repo.CreateUser(ctx, &User{
		Username: "alice", Email: "alice@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("first CreateUser() error = %v", err)
	}

	_, err := repo.CreateUser(ctx, &User{
		Username: "bob", Email: "alice@example.com", PasswordHash: "hash",
	})

	if !errors.Is(err, ErrEmailTaken) {
		t.Fatalf("CreateUser() error = %v, want ErrEmailTaken", err)
	}
}

func TestCreateUserAllowsADifferentUsernameAndEmail(t *testing.T) {
	repo := newTestRepository(t)
	ctx := context.Background()

	if _, err := repo.CreateUser(ctx, &User{
		Username: "alice", Email: "alice@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("first CreateUser() error = %v", err)
	}

	if _, err := repo.CreateUser(ctx, &User{
		Username: "bob", Email: "bob@example.com", PasswordHash: "hash",
	}); err != nil {
		t.Fatalf("second CreateUser() error = %v, want nil", err)
	}
}

// TestRegisterAgainstPostgres exercises the service and the real database
// together: the row that lands in Postgres must hold a bcrypt hash of the
// submitted password, and never the password itself.
func TestRegisterAgainstPostgres(t *testing.T) {
	repo := newTestRepository(t)
	svc := NewService(repo, bcrypt.MinCost)
	ctx := context.Background()

	const password = "hunter2secret"
	user, err := svc.Register(ctx, RegisterInput{
		Username: "  Alice  ", Email: "  Alice@EXAMPLE.com  ", Password: password,
	})
	if err != nil {
		t.Fatalf("Register() error = %v, want nil", err)
	}

	var username, email, storedHash string
	err = repo.db.QueryRowContext(ctx,
		"SELECT username, email, password_hash FROM users WHERE id = $1", user.ID,
	).Scan(&username, &email, &storedHash)
	if err != nil {
		t.Fatalf("reading back the row: %v", err)
	}

	if username != "Alice" {
		t.Errorf("stored username = %q, want %q", username, "Alice")
	}
	if email != "alice@example.com" {
		t.Errorf("stored email = %q, want %q", email, "alice@example.com")
	}
	if storedHash == password {
		t.Fatal("the plaintext password was written to the database")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(password)); err != nil {
		t.Fatalf("the stored hash does not verify against the password: %v", err)
	}

	// The registration must now be unrepeatable in either field.
	_, err = svc.Register(ctx, RegisterInput{
		Username: "alice", Email: "different@example.com", Password: password,
	})
	if !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("re-registering the username: error = %v, want ErrUsernameTaken", err)
	}

	_, err = svc.Register(ctx, RegisterInput{
		Username: "different", Email: "alice@example.com", Password: password,
	})
	if !errors.Is(err, ErrEmailTaken) {
		t.Errorf("re-registering the email: error = %v, want ErrEmailTaken", err)
	}
}
