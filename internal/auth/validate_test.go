package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalizeRegisterInput(t *testing.T) {
	in := RegisterInput{
		Username: "  Alice  ",
		Email:    "  Alice@EXAMPLE.com  ",
		Password: "  hunter2secret  ",
	}

	got := normalizeRegisterInput(in)

	if got.Username != "Alice" {
		t.Errorf("username: got %q, want %q (surrounding whitespace trimmed, casing kept)", got.Username, "Alice")
	}
	if got.Email != "alice@example.com" {
		t.Errorf("email: got %q, want %q (trimmed and lowercased)", got.Email, "alice@example.com")
	}
	if got.Password != "  hunter2secret  " {
		t.Errorf("password: got %q, want it untouched — whitespace is significant in a password", got.Password)
	}
}

func TestValidateRegisterInputAcceptsValidInput(t *testing.T) {
	valid := []RegisterInput{
		{Username: "alice", Email: "alice@example.com", Password: "hunter2secret"},
		{Username: "abc", Email: "a@b.co", Password: "12345678"},
		{Username: strings.Repeat("u", 32), Email: "first.last+tag@sub.example.co.uk", Password: "pässwörd"},
		{Username: "Alice_99", Email: "a@b.com", Password: strings.Repeat("x", 72)},
	}

	for _, in := range valid {
		if err := validateRegisterInput(in); err != nil {
			t.Errorf("validateRegisterInput(%+v) = %v, want nil", in, err)
		}
	}
}

func TestValidateRegisterInputRejectsBadUsernames(t *testing.T) {
	tests := map[string]string{
		"empty":           "",
		"whitespace only": "   ",
		"too short":       "ab",
		"too long":        strings.Repeat("u", 33),
		"contains space":  "bad name",
		"contains hyphen": "bad-name",
		"contains symbol": "bad!name",
		"non ascii":       "ユーザー名",
	}

	for name, username := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRegisterInput(RegisterInput{
				Username: username,
				Email:    "a@b.com",
				Password: "hunter2secret",
			})
			assertFieldInvalid(t, err, "username")
		})
	}
}

func TestValidateRegisterInputRejectsBadEmails(t *testing.T) {
	tests := map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"no at sign":       "notanemail",
		"no domain":        "alice@",
		"no local part":    "@example.com",
		"no dot in domain": "alice@example",
		"display name":     "Alice <alice@example.com>",
		"two addresses":    "alice@example.com, bob@example.com",
		"internal space":   "ali ce@example.com",
		"trailing dot":     "alice@example.com.",
		"too long":         strings.Repeat("a", 250) + "@example.com",
	}

	for name, email := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRegisterInput(RegisterInput{
				Username: "alice",
				Email:    email,
				Password: "hunter2secret",
			})
			assertFieldInvalid(t, err, "email")
		})
	}
}

func TestValidateRegisterInputRejectsBadPasswords(t *testing.T) {
	tests := map[string]string{
		"empty":                 "",
		"seven characters":      "1234567",
		"seventy three bytes":   strings.Repeat("x", 73),
		"over 72 bytes in utf8": strings.Repeat("é", 37), // 74 bytes, only 37 runes
	}

	for name, password := range tests {
		t.Run(name, func(t *testing.T) {
			err := validateRegisterInput(RegisterInput{
				Username: "alice",
				Email:    "a@b.com",
				Password: password,
			})
			assertFieldInvalid(t, err, "password")
		})
	}
}

func TestValidateRegisterInputReportsEveryInvalidFieldAtOnce(t *testing.T) {
	err := validateRegisterInput(RegisterInput{Username: "", Email: "nope", Password: "short"})

	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("got %v, want a *ValidationError", err)
	}

	for _, field := range []string{"username", "email", "password"} {
		if _, ok := vErr.Fields[field]; !ok {
			t.Errorf("Fields is missing %q; got %v", field, vErr.Fields)
		}
	}
}

func TestValidationErrorMessageNamesEveryInvalidField(t *testing.T) {
	err := &ValidationError{Fields: map[string]string{
		"username": "is required",
		"email":    "is not a valid email address",
	}}

	msg := err.Error()

	for _, want := range []string{"username", "is required", "email", "is not a valid email address"} {
		if !strings.Contains(msg, want) {
			t.Errorf("Error() = %q, want it to contain %q", msg, want)
		}
	}
}

// assertFieldInvalid fails the test unless err is a *ValidationError carrying
// an entry for field.
func assertFieldInvalid(t *testing.T, err error, field string) {
	t.Helper()

	var vErr *ValidationError
	if !errors.As(err, &vErr) {
		t.Fatalf("got %v, want a *ValidationError naming %q", err, field)
	}
	if _, ok := vErr.Fields[field]; !ok {
		t.Fatalf("Fields = %v, want an entry for %q", vErr.Fields, field)
	}
}
