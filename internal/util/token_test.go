package util

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testSecret = "a-test-secret-that-is-long-enough-32"

func testSubject() TokenSubject {
	return TokenSubject{UserID: 7, Username: "alice", Email: "alice@example.com"}
}

func newTestIssuer(t *testing.T) *TokenIssuer {
	t.Helper()
	issuer, err := NewTokenIssuer(testSecret, 15*time.Minute)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v, want nil", err)
	}
	return issuer
}

func TestNewTokenIssuerRejectsAnUnusableConfiguration(t *testing.T) {
	tests := map[string]struct {
		secret string
		ttl    time.Duration
	}{
		"empty secret":    {"", time.Minute},
		"zero ttl":        {testSecret, 0},
		"negative ttl":    {testSecret, -time.Minute},
		"whitespace only": {"   ", time.Minute},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := NewTokenIssuer(tc.secret, tc.ttl); err == nil {
				t.Error("NewTokenIssuer() = nil error, want a rejection")
			}
		})
	}
}

func TestIssueAndParseRoundTripTheClaims(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.Issue(testSubject())
	if err != nil {
		t.Fatalf("Issue() error = %v, want nil", err)
	}

	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("Parse() error = %v, want nil", err)
	}

	if claims.UserID != 7 {
		t.Errorf("UserID = %d, want 7", claims.UserID)
	}
	if claims.Username != "alice" {
		t.Errorf("Username = %q, want %q", claims.Username, "alice")
	}
	if claims.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", claims.Email, "alice@example.com")
	}
	if claims.Subject != "7" {
		t.Errorf("Subject = %q, want %q", claims.Subject, "7")
	}
}

func TestIssueSetsExpiryFromTheConfiguredLifetime(t *testing.T) {
	issuer := newTestIssuer(t)
	before := time.Now()

	token, err := issuer.Issue(testSubject())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	claims, err := issuer.Parse(token)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	if claims.ExpiresAt == nil {
		t.Fatal("ExpiresAt is not set")
	}
	got := claims.ExpiresAt.Time
	earliest, latest := before.Add(15*time.Minute), time.Now().Add(15*time.Minute)
	if got.Before(earliest.Add(-time.Second)) || got.After(latest.Add(time.Second)) {
		t.Errorf("ExpiresAt = %v, want within [%v, %v]", got, earliest, latest)
	}
	if claims.IssuedAt == nil || claims.IssuedAt.IsZero() {
		t.Error("IssuedAt is not set")
	}
}

func TestIssueGivesEveryTokenItsOwnIdentifier(t *testing.T) {
	issuer := newTestIssuer(t)

	first, err := issuer.Issue(testSubject())
	if err != nil {
		t.Fatalf("first Issue() error = %v", err)
	}
	second, err := issuer.Issue(testSubject())
	if err != nil {
		t.Fatalf("second Issue() error = %v", err)
	}

	firstClaims, _ := issuer.Parse(first)
	secondClaims, _ := issuer.Parse(second)

	if firstClaims.ID == "" {
		t.Fatal("ID (jti) is empty, want a unique identifier per token")
	}
	if firstClaims.ID == secondClaims.ID {
		t.Error("two tokens share a jti; each must be individually identifiable")
	}
}

func TestParseRejectsATokenSignedWithAnotherSecret(t *testing.T) {
	other, err := NewTokenIssuer("a-completely-different-secret-key-32", 15*time.Minute)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	token, err := other.Issue(testSubject())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	_, err = newTestIssuer(t).Parse(token)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse() error = %v, want ErrInvalidToken", err)
	}
}

func TestParseRejectsAnExpiredToken(t *testing.T) {
	issuer, err := NewTokenIssuer(testSecret, time.Nanosecond)
	if err != nil {
		t.Fatalf("NewTokenIssuer() error = %v", err)
	}
	token, err := issuer.Issue(testSubject())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	time.Sleep(2 * time.Millisecond)

	_, err = issuer.Parse(token)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse() error = %v, want ErrInvalidToken", err)
	}
}

// TestParseRejectsAnUnsignedToken covers the "alg: none" forgery: a library
// that trusts the header's algorithm will accept a token nobody signed.
func TestParseRejectsAnUnsignedToken(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	forged := b64(`{"alg":"none","typ":"JWT"}`) + "." +
		b64(`{"uid":7,"username":"alice","email":"alice@example.com","sub":"7","exp":99999999999}`) + "."

	_, err := newTestIssuer(t).Parse(forged)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse() error = %v, want ErrInvalidToken for an unsigned token", err)
	}
}

// TestParseRejectsAnotherHMACAlgorithm covers algorithm substitution: the
// signature verifies against our secret, but the algorithm is not the one we
// issue with, so it must still be refused.
func TestParseRejectsAnotherHMACAlgorithm(t *testing.T) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS512, &Claims{
		UserID: 7, Username: "alice", Email: "alice@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "7",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	})
	signed, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("signing: %v", err)
	}

	_, err = newTestIssuer(t).Parse(signed)

	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Parse() error = %v, want ErrInvalidToken for HS512", err)
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	for name, token := range map[string]string{
		"empty":         "",
		"not a jwt":     "hello",
		"two segments":  "aaa.bbb",
		"bad base64":    "!!!.!!!.!!!",
		"empty segment": "..",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newTestIssuer(t).Parse(token); !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Parse(%q) error = %v, want ErrInvalidToken", token, err)
			}
		})
	}
}

func TestTTLReportsTheConfiguredLifetime(t *testing.T) {
	if got := newTestIssuer(t).TTL(); got != 15*time.Minute {
		t.Errorf("TTL() = %v, want 15m", got)
	}
}

func TestExtractBearerTokenReadsTheHeader(t *testing.T) {
	for name, header := range map[string]string{
		"canonical scheme": "Bearer abc.def.ghi",
		"lowercase scheme": "bearer abc.def.ghi",
		"uppercase scheme": "BEARER abc.def.ghi",
		"extra spaces":     "Bearer    abc.def.ghi   ",
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ExtractBearerToken(header)
			if err != nil {
				t.Fatalf("ExtractBearerToken(%q) error = %v, want nil", header, err)
			}
			if got != "abc.def.ghi" {
				t.Errorf("got %q, want %q", got, "abc.def.ghi")
			}
		})
	}
}

func TestExtractBearerTokenRejectsUnusableHeaders(t *testing.T) {
	for name, header := range map[string]string{
		"empty":            "",
		"whitespace only":  "   ",
		"scheme only":      "Bearer",
		"scheme and space": "Bearer   ",
		"wrong scheme":     "Basic YWxpY2U6c2VjcmV0",
		"no scheme":        "abc.def.ghi",
		"token with space": "Bearer abc def",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ExtractBearerToken(header); !errors.Is(err, ErrMissingBearerToken) {
				t.Errorf("ExtractBearerToken(%q) error = %v, want ErrMissingBearerToken", header, err)
			}
		})
	}
}

func TestIssuedTokenLooksLikeAJWT(t *testing.T) {
	token, err := newTestIssuer(t).Issue(testSubject())
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}

	if n := strings.Count(token, "."); n != 2 {
		t.Errorf("token has %d dots, want 2: %q", n, token)
	}
	if strings.Contains(token, " ") {
		t.Errorf("token contains a space: %q", token)
	}
}
