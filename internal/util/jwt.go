package util

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

var ErrInvalidToken = errors.New("invalid token")

type AccessTokenClaims struct {
	UserID    uuid.UUID
	ExpiresAt time.Time
}

func GenerateJWT(secret string, userID uuid.UUID, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", fmt.Errorf("signing token: %w", err)
	}
	return signed, nil
}

func ParseJWT(secret, tokenString string) (uuid.UUID, error) {
	claims, err := ParseAccessToken(secret, tokenString)
	if err != nil {
		return uuid.Nil, err
	}
	return claims.UserID, nil
}

func ParseAccessToken(secret, tokenString string) (AccessTokenClaims, error) {
	claims := &jwt.RegisteredClaims{}
	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		func(t *jwt.Token) (any, error) {
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, fmt.Errorf("unexpected signing method %q", t.Method.Alg())
			}
			return []byte(secret), nil
		},
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return AccessTokenClaims{}, ErrInvalidToken
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil || claims.ExpiresAt == nil {
		return AccessTokenClaims{}, ErrInvalidToken
	}
	return AccessTokenClaims{
		UserID:    userID,
		ExpiresAt: claims.ExpiresAt.Time,
	}, nil
}
