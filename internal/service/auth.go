package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/domain"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/repository"
	"github.com/prompt-masters/backend/internal/util"
)

const (
	TokenTTL   = 24 * time.Hour
	tokenBytes = 32
)

type RegisterInput struct {
	Username string
	Email    string
	Password string
}

type Registration struct {
	User              *domain.User
	VerificationToken string
}

type LoginInput struct {
	Email    string
	Password string
}

type LoginResult struct {
	User         *domain.User
	AccessToken  string
	RefreshToken string
}

type AuthService struct {
	users   repository.UserRepository
	tokens  repository.VerificationTokenRepository
	refresh repository.RefreshTokenRepository
	sender  mail.Sender
	cfg     config.Config
}

func NewAuthService(
	users repository.UserRepository,
	tokens repository.VerificationTokenRepository,
	refresh repository.RefreshTokenRepository,
	sender mail.Sender,
	cfg config.Config,
) *AuthService {
	return &AuthService{users: users, tokens: tokens, refresh: refresh, sender: sender, cfg: cfg}
}

func (s *AuthService) Register(ctx context.Context, in RegisterInput) (*Registration, error) {
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
		return nil, err
	}

	rawToken, err := util.GenerateToken(tokenBytes)
	if err != nil {
		return nil, fmt.Errorf("generating verification token: %w", err)
	}

	_, err = s.tokens.Create(ctx, created.ID, rawToken, time.Now().Add(TokenTTL))
	if err != nil {
		return nil, fmt.Errorf("storing verification token: %w", err)
	}

	link := strings.TrimSuffix(s.cfg.AppBaseURL, "/") + "/api/v1/auth/verify-email?token=" + rawToken
	go func() {
		_ = s.sender.SendVerification(context.Background(), created.Email, created.Username, link)
	}()

	return &Registration{User: created, VerificationToken: rawToken}, nil
}

func (s *AuthService) Login(ctx context.Context, in LoginInput) (*LoginResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if err := validateLoginInput(email, in.Password); err != nil {
		return nil, err
	}

	user, err := s.users.GetByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrInvalidCredentials
		}
		return nil, fmt.Errorf("looking up user: %w", err)
	}

	if !util.CheckPassword(user.Password, in.Password) {
		return nil, domain.ErrInvalidCredentials
	}

	if !user.EmailVerified {
		return nil, domain.ErrEmailNotVerified
	}

	token, err := util.GenerateJWT(s.cfg.JWTSecret, user.ID, s.cfg.JWTTTL)
	if err != nil {
		return nil, fmt.Errorf("generating access token: %w", err)
	}

	refreshToken, err := util.GenerateToken(tokenBytes)
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}
	if err := s.refresh.Create(ctx, util.HashToken(refreshToken), user.ID, time.Now().Add(s.cfg.RefreshTokenTTL)); err != nil {
		return nil, fmt.Errorf("storing refresh token: %w", err)
	}

	return &LoginResult{User: user, AccessToken: token, RefreshToken: refreshToken}, nil
}

func (s *AuthService) VerifyEmail(ctx context.Context, rawToken string) (*domain.User, error) {
	if rawToken == "" {
		return nil, domain.ErrInvalidVerificationToken
	}

	token, err := s.tokens.GetByToken(ctx, rawToken)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil, domain.ErrInvalidVerificationToken
		}
		return nil, fmt.Errorf("looking up verification token: %w", err)
	}

	if time.Now().After(token.ExpiresAt) {
		return nil, domain.ErrVerificationTokenExpired
	}

	if err := s.users.SetEmailVerified(ctx, token.UserID); err != nil {
		return nil, fmt.Errorf("marking email verified: %w", err)
	}

	if err := s.tokens.DeleteByToken(ctx, rawToken); err != nil {
		return nil, fmt.Errorf("deleting verification token: %w", err)
	}

	return s.users.GetByID(ctx, token.UserID)
}

func (s *AuthService) Me(ctx context.Context, userID uuid.UUID) (*domain.User, error) {
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	return user, nil
}

type UpdateProfileInput struct {
	Username string
}

func (s *AuthService) UpdateProfile(ctx context.Context, userID uuid.UUID, in UpdateProfileInput) (*domain.User, error) {
	in.Username = strings.TrimSpace(in.Username)
	if err := validateUsernameInput(in.Username); err != nil {
		return nil, err
	}

	if err := s.users.UpdateUsername(ctx, userID, in.Username); err != nil {
		return nil, err
	}
	return s.users.GetByID(ctx, userID)
}

func (s *AuthService) Refresh(ctx context.Context, rawRefreshToken string) (*LoginResult, error) {
	if rawRefreshToken == "" {
		return nil, domain.ErrInvalidRefreshToken
	}
	hash := util.HashToken(rawRefreshToken)
	userID, err := s.refresh.GetActiveUserID(ctx, hash)
	if err != nil {
		return nil, err
	}
	user, err := s.users.GetByID(ctx, userID)
	if err != nil {
		return nil, err
	}
	accessToken, err := util.GenerateJWT(s.cfg.JWTSecret, user.ID, s.cfg.JWTTTL)
	if err != nil {
		return nil, fmt.Errorf("generating access token: %w", err)
	}
	newRefreshToken, err := util.GenerateToken(tokenBytes)
	if err != nil {
		return nil, fmt.Errorf("generating refresh token: %w", err)
	}
	rotatedUserID, err := s.refresh.Rotate(ctx, hash, util.HashToken(newRefreshToken), time.Now().Add(s.cfg.RefreshTokenTTL))
	if err != nil {
		return nil, err
	}
	if rotatedUserID != userID {
		return nil, domain.ErrInvalidRefreshToken
	}

	return &LoginResult{User: user, AccessToken: accessToken, RefreshToken: newRefreshToken}, nil
}

func (s *AuthService) Logout(ctx context.Context, rawRefreshToken string) error {
	if rawRefreshToken == "" {
		return nil
	}
	return s.refresh.Revoke(ctx, util.HashToken(rawRefreshToken))
}
