package domain

import (
	"time"

	"github.com/google/uuid"
)

type VerificationToken struct {
	ID        int64     `json:"id"`
	UserID    uuid.UUID `json:"user_id"`
	Token     string    `json:"-"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}
