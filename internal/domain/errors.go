package domain

import "errors"

var (
	ErrNotFound                 = errors.New("not found")
	ErrEmailTaken               = errors.New("email already registered")
	ErrUsernameTaken            = errors.New("username already taken")
	ErrInvalidCredentials       = errors.New("invalid credentials")
	ErrEmailNotVerified         = errors.New("email not verified")
	ErrInvalidVerificationToken = errors.New("verification token is invalid")
	ErrVerificationTokenExpired = errors.New("verification token has expired")
)
