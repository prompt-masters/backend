// Package util holds the small, reusable pieces the HTTP layer needs but the
// domain does not: signing and validating access tokens, and reading a bearer
// token off a request header. It depends on no other package in this module,
// so anything may import it.
package util

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	// ErrInvalidToken covers every reason a token failed: bad signature,
	// expired, malformed, wrong algorithm. They are deliberately
	// indistinguishable, since a caller can do nothing different about any
	// of them and the difference is useful only to an attacker.
	ErrInvalidToken = errors.New("util: token is invalid")

	ErrMissingBearerToken = errors.New("util: no bearer token in the Authorization header")
)

// signingMethod is the one algorithm we issue and the only one we accept.
var signingMethod = jwt.SigningMethodHS256

// minSecretBytes is the shortest secret NewTokenIssuer will not warn about.
// HS256 keys shorter than the hash output add nothing to the security of the
// construction.
const minSecretBytes = 32

// Claims is the payload of an access token. The registered claims carry sub,
// exp, iat and jti.
type Claims struct {
	UserID   int64  `json:"uid"`
	Username string `json:"username"`
	Email    string `json:"email"`
	jwt.RegisteredClaims
}

// TokenSubject is who a token is being issued for. It is a plain struct
// rather than a domain type so this package stays dependency-free.
type TokenSubject struct {
	UserID   int64
	Username string
	Email    string
}

// TokenIssuer signs and validates access tokens.
type TokenIssuer struct {
	secret []byte
	ttl    time.Duration
}

// NewTokenIssuer returns an issuer signing with secret for ttl. It rejects an
// empty secret and a non-positive lifetime; a short secret is the caller's
// business to warn about via SecretIsWeak.
func NewTokenIssuer(secret string, ttl time.Duration) (*TokenIssuer, error) {
	if strings.TrimSpace(secret) == "" {
		return nil, errors.New("util: token secret is required")
	}
	if ttl <= 0 {
		return nil, fmt.Errorf("util: token lifetime must be positive, got %v", ttl)
	}
	return &TokenIssuer{secret: []byte(secret), ttl: ttl}, nil
}

// SecretIsWeak reports whether a secret is shorter than the signing hash, so
// startup can warn without refusing to run.
func SecretIsWeak(secret string) bool { return len(secret) < minSecretBytes }

// TTL is the lifetime tokens are issued with, which a caller needs in order
// to report expires_in.
func (i *TokenIssuer) TTL() time.Duration { return i.ttl }

// Issue returns a signed access token for sub.
func (i *TokenIssuer) Issue(sub TokenSubject) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", fmt.Errorf("util: generating token id: %w", err)
	}

	now := time.Now()
	token := jwt.NewWithClaims(signingMethod, &Claims{
		UserID:   sub.UserID,
		Username: sub.Username,
		Email:    sub.Email,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   strconv.FormatInt(sub.UserID, 10),
			ID:        id,
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
		},
	})

	signed, err := token.SignedString(i.secret)
	if err != nil {
		return "", fmt.Errorf("util: signing token: %w", err)
	}
	return signed, nil
}

// Parse validates a token and returns its claims, or ErrInvalidToken.
//
// The accepted algorithm is pinned to the one we issue with. Without that
// pin a token whose header says "alg":"none" would be accepted unsigned, and
// one signed with a different algorithm would be accepted too — the classic
// algorithm-confusion forgeries.
func (i *TokenIssuer) Parse(raw string) (*Claims, error) {
	var claims Claims

	_, err := jwt.ParseWithClaims(raw, &claims,
		func(t *jwt.Token) (any, error) { return i.secret, nil },
		jwt.WithValidMethods([]string{signingMethod.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidToken, err)
	}
	return &claims, nil
}

// ExtractBearerToken returns the token from an Authorization header value.
// The scheme is matched case-insensitively, as RFC 7235 requires.
func ExtractBearerToken(header string) (string, error) {
	scheme, token, found := strings.Cut(strings.TrimSpace(header), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", ErrMissingBearerToken
	}

	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t") {
		return "", ErrMissingBearerToken
	}
	return token, nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
