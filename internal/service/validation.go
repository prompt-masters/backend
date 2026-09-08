package service

import (
	"fmt"
	stdmail "net/mail"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	UsernameMinLength = 3
	UsernameMaxLength = 32
	PasswordMinLength = 8
	PasswordMaxBytes  = 72
	EmailMaxLength    = 254
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	return "validation failed"
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

func validateLoginInput(email, password string) error {
	fields := make(map[string]string)

	if strings.TrimSpace(email) == "" {
		fields["email"] = "is required"
	}
	if password == "" {
		fields["password"] = "is required"
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

func validateUsernameInput(username string) error {
	if msg := validateUsername(username); msg != "" {
		return &ValidationError{Fields: map[string]string{"username": msg}}
	}
	return nil
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
