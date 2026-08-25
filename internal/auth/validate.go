package auth

import (
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// UsernameMinLength and UsernameMaxLength bound a username, counted in
	// characters. The maximum matches the users.username column width.
	UsernameMinLength = 3
	UsernameMaxLength = 32

	// PasswordMinLength is the minimum password length in characters.
	PasswordMinLength = 8

	// PasswordMaxBytes is bcrypt's hard input limit. bcrypt silently
	// truncates anything longer, which would make the extra bytes
	// meaningless, so we reject instead of quietly ignoring them.
	PasswordMaxBytes = 72

	// EmailMaxLength is the longest address permitted by RFC 5321.
	EmailMaxLength = 254
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)

// RegisterInput is the raw, untrusted input to a registration attempt.
type RegisterInput struct {
	Username string
	Email    string
	Password string
}

// ValidationError reports every field of a RegisterInput that failed
// validation, keyed by field name.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	fields := make([]string, 0, len(e.Fields))
	for field := range e.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)

	parts := make([]string, 0, len(fields))
	for _, field := range fields {
		parts = append(parts, fmt.Sprintf("%s %s", field, e.Fields[field]))
	}
	return "invalid registration input: " + strings.Join(parts, "; ")
}

// normalizeRegisterInput puts input into the form it is stored and compared
// in: usernames keep their casing but lose surrounding whitespace, emails are
// lowercased so the unique index makes them case-insensitively unique.
// Passwords are left exactly as typed — whitespace is significant.
func normalizeRegisterInput(in RegisterInput) RegisterInput {
	return RegisterInput{
		Username: strings.TrimSpace(in.Username),
		Email:    strings.ToLower(strings.TrimSpace(in.Email)),
		Password: in.Password,
	}
}

// validateRegisterInput checks every field and reports all failures at once,
// so a caller can show a user everything wrong with their submission rather
// than one problem per attempt. It returns nil or a *ValidationError.
func validateRegisterInput(in RegisterInput) error {
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

	const invalid = "is not a valid email address"

	addr, err := mail.ParseAddress(email)
	if err != nil {
		return invalid
	}
	// ParseAddress accepts "Alice <alice@example.com>". We want a bare
	// address, so anything it had to strip means the input was not one.
	if addr.Name != "" || addr.Address != email {
		return invalid
	}

	at := strings.LastIndex(email, "@")
	local, domain := email[:at], email[at+1:]
	if local == "" || domain == "" {
		return invalid
	}
	// A domain with no dot ("alice@example") is syntactically legal but is
	// never a real address; a leading or trailing dot is malformed.
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return invalid
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
