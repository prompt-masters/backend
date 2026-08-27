package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	stdmail "net/mail"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/repository"
	"github.com/prompt-masters/backend/internal/util"
)

var (
	ErrUsernameTaken      = errors.New("username already taken")
	ErrEmailTaken         = errors.New("email already registered")
	ErrInvalidToken       = errors.New("verification token is invalid")
	ErrTokenExpired       = errors.New("verification token has expired")
	ErrInvalidCredentials = errors.New("invalid credentials")
)

const (
	UsernameMinLength = 3
	UsernameMaxLength = 32
	PasswordMinLength = 8
	PasswordMaxBytes  = 72
	EmailMaxLength    = 254

	TokenTTL          = 24 * time.Hour
	tokenBytes        = 32
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return "validation failed"
}

type RegisterInput struct {
	Username string
	Email    string
	Password string
}

type Registration struct {
	User              *domain.User
	VerificationToken string
}

type AuthService struct {
	users  repository.UserRepository
	tokens repository.VerificationTokenRepository
	sender mail.Sender
}

func NewAuthService(
	users repository.UserRepository,
	tokens repository.VerificationTokenRepository,
	sender mail.Sender,
) *AuthService {
	return &AuthService{users: users, tokens: tokens, sender: sender}
}

func (s *AuthService) Register(ctx context.Context, in RegisterInput, baseURL string) (*Registration, error) {
	in = normalizeInput(in)

	if err := validateInput(in); err != nil {
		return nil, err
	}

	hash, err := util.HashPassword(in.Password)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}

	user := &domain.User{
		Username: in.Username,
		Email:    in.Email,
		Password: hash,
	}

	created, err := s.users.Create(ctx, user)
	if err != nil {
		return nil, fmt.Errorf("creating user: %w", err)
	}

	rawToken, err := generateToken()
	if err != nil {
		return nil, fmt.Errorf("generating verification token: %w", err)
	}

	_, err = s.tokens.Create(ctx, created.ID, rawToken, time.Now().Add(TokenTTL))
	if err != nil {
		return nil, fmt.Errorf("storing verification token: %w", err)
	}

	link := strings.TrimSuffix(baseURL, "/") + "/api/v1/auth/verify-email?token=" + rawToken
	go func() {
		_ = s.sender.SendVerification(context.Background(), created.Email, created.Username, link)
	}()

	return &Registration{User: created, VerificationToken: rawToken}, nil
}

func (s *AuthService) VerifyEmail(ctx context.Context, rawToken string) (*domain.User, error) {
	if rawToken == "" {
		return nil, ErrInvalidToken
	}

	token, err := s.tokens.GetByToken(ctx, rawToken)
	if err != nil {
		return nil, ErrInvalidToken
	}

	if time.Now().After(token.ExpiresAt) {
		return nil, ErrTokenExpired
	}

	if err := s.users.SetEmailVerified(ctx, token.UserID); err != nil {
		return nil, fmt.Errorf("marking email verified: %w", err)
	}

	if err := s.tokens.DeleteByToken(ctx, rawToken); err != nil {
		return nil, fmt.Errorf("deleting verification token: %w", err)
	}

	return s.users.GetByID(ctx, token.UserID)
}

func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func normalizeInput(in RegisterInput) RegisterInput {
	return RegisterInput{
		Username: strings.TrimSpace(in.Username),
		Email:    strings.ToLower(strings.TrimSpace(in.Email)),
		Password: in.Password,
	}
}

func validateInput(in RegisterInput) error {
	fields := make(map[string]string)

	if msg := validateUsername(in.Username); msg != "" {
		fields["username"] = msg
	}
	if msg := validateEmail(in.Email); msg != "" {
		fields["email"] = msg
	}
	if msg := validatePassword(in.Password); msg != "" {
		fields["password"] = msg
	}

	if len(fields) == 0 {
		return nil
	}
	return &ValidationError{Fields: fields}
}

func validateUsername(username string) string {
	switch {
	case strings.TrimSpace(username) == "":
		return "is required"
	case utf8.RuneCountInString(username) < UsernameMinLength:
		return fmt.Sprintf("must be at least %d characters", UsernameMinLength)
	case utf8.RuneCountInString(username) > UsernameMaxLength:
		return fmt.Sprintf("must not exceed %d characters", UsernameMaxLength)
	case !usernamePattern.MatchString(username):
		return "may contain only letters, digits and underscores"
	}
	return ""
}

func validateEmail(email string) string {
	if strings.TrimSpace(email) == "" {
		return "is required"
	}
	if len(email) > EmailMaxLength {
		return fmt.Sprintf("must not exceed %d characters", EmailMaxLength)
	}

	addr, err := stdmail.ParseAddress(email)
	if err != nil || addr.Name != "" || addr.Address != email {
		return "is not a valid email address"
	}

	at := strings.LastIndex(email, "@")
	domain := email[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "is not a valid email address"
	}
	return ""
}

func validatePassword(password string) string {
	switch {
	case password == "":
		return "is required"
	case utf8.RuneCountInString(password) < PasswordMinLength:
		return fmt.Sprintf("must be at least %d characters", PasswordMinLength)
	case len(password) > PasswordMaxBytes:
		return fmt.Sprintf("must not exceed %d bytes", PasswordMaxBytes)
	}
	return ""
}
