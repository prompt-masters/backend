package util

import "github.com/alexedwards/argon2id"

func HashPassword(plain string) (string, error) {
	return argon2id.CreateHash(plain, argon2id.DefaultParams)
}

func CheckPassword(hashed, plain string) bool {
	ok, _ := argon2id.ComparePasswordAndHash(plain, hashed)
	return ok
}
