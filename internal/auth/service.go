package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	// DefaultBcryptCost is the work factor to use outside of tests.
	DefaultBcryptCost = bcrypt.DefaultCost

	// VerificationTokenTTL is how long a verification link stays usable.
	VerificationTokenTTL = 24 * time.Hour

	// verificationTokenBytes is the entropy behind a verification token.
	verificationTokenBytes = 32
)

// Service registers new users and verifies their email addresses.
type Service struct {
	repo       Repository
	bcryptCost int

	// decoyHash is compared against when a login names an address that does
	// not exist, so that path spends the same time in bcrypt as a real one.
	decoyHash []byte
}

// NewService builds a Service. Pass DefaultBcryptCost in production; tests
// pass bcrypt.MinCost so they do not pay the production work factor.
func NewService(repo Repository, bcryptCost int) *Service {
	// Computed once at construction rather than per request; see Login.
	decoy, err := bcrypt.GenerateFromPassword([]byte("login timing decoy"), bcryptCost)
	if err != nil {
		decoy = nil
	}
	return &Service{repo: repo, bcryptCost: bcryptCost, decoyHash: decoy}
}

// Register validates the input, hashes the password with bcrypt, and persists
// the account together with a single-use email verification token. The
// account starts unverified and rated DefaultEloRating.
//
// It returns a *ValidationError if the input is malformed, ErrUsernameTaken
// or ErrEmailTaken if the account already exists, and otherwise a
// Registration holding the persisted user and the raw token to email. The
// plaintext password is never stored, logged or returned.
//
// Uniqueness is decided by the database's unique indexes rather than by a
// prior lookup, so two concurrent registrations cannot both win.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*Registration, error) {
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

	rawToken, err := generateVerificationToken()
	if err != nil {
		return nil, fmt.Errorf("generating verification token: %w", err)
	}

	user, err := s.repo.CreateUserWithVerificationToken(ctx,
		&User{
			Username:      in.Username,
			Email:         in.Email,
			PasswordHash:  string(hash),
			EloRating:     DefaultEloRating,
			EmailVerified: false,
		},
		&VerificationToken{
			Hash:      hashVerificationToken(rawToken),
			ExpiresAt: time.Now().Add(VerificationTokenTTL),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	return &Registration{User: user, VerificationToken: rawToken}, nil
}

// VerifyEmail spends the token from a verification link and marks the account
// verified. It returns ErrInvalidToken for an unknown, malformed or
// already-spent token, and ErrTokenExpired for one past its lifetime.
func (s *Service) VerifyEmail(ctx context.Context, rawToken string) (*User, error) {
	if rawToken == "" {
		return nil, ErrInvalidToken
	}

	user, err := s.repo.VerifyEmail(ctx, hashVerificationToken(rawToken))
	if err != nil {
		return nil, fmt.Errorf("verifying email: %w", err)
	}
	return user, nil
}

// generateVerificationToken returns a URL-safe token backed by
// verificationTokenBytes of cryptographically random data, suitable for
// putting straight into a link.
func generateVerificationToken() (string, error) {
	b := make([]byte, verificationTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// hashVerificationToken reduces a raw token to what we are willing to store.
// SHA-256 is right here where bcrypt is right for passwords: the input is
// already high-entropy random, so there is nothing to brute-force and no
// reason to pay a work factor on every verification request.
func hashVerificationToken(rawToken string) []byte {
	sum := sha256.Sum256([]byte(rawToken))
	return sum[:]
}

// Login checks an email and password and returns the account.
//
// Every failure — unknown address, wrong password, unverified email — is
// reported as ErrInvalidCredentials. Distinguishing them would let anyone
// discover which addresses have accounts, and there is nothing a legitimate
// caller could do differently in each case.
//
// An unknown address still pays for a bcrypt comparison, against a decoy
// hash. Skipping it would return in microseconds where a real account takes
// tens of milliseconds, and that gap is enough to enumerate the user base.
func (s *Service) Login(ctx context.Context, in LoginInput) (*User, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))

	user, err := s.repo.FindUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			s.spendDecoyComparison(in.Password)
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("finding user by email: %w", err)
	}

	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(in.Password)) != nil {
		return nil, ErrInvalidCredentials
	}

	// Checked after the password so an unverified account costs the same as
	// any other failure.
	if !user.EmailVerified {
		return nil, ErrInvalidCredentials
	}

	return user, nil
}

func (s *Service) spendDecoyComparison(password string) {
	if s.decoyHash != nil {
		_ = bcrypt.CompareHashAndPassword(s.decoyHash, []byte(password))
	}
}
