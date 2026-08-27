package auth

import (
	"github.com/alexedwards/argon2id"
)

func Hash(password string) (string, error) {
	hashedPassword, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return "", err
	}
	return hashedPassword, nil
}

func Check(password, hash string) (bool, error) {
	return argon2id.ComparePasswordAndHash(password, hash)
}
