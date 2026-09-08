package util

import (
	"crypto/rand"
	"encoding/base64"
)

func GenerateToken(tokenBytes int) (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
