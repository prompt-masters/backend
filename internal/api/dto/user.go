package dto

import (
	"time"

	"github.com/google/uuid"
	"github.com/prompt-masters/backend/internal/domain"
)

type CurrentUserResponse struct {
	ID        uuid.UUID `json:"id"`
	Username  string    `json:"username"`
	Email     string    `json:"email"`
	AvatarURL *string   `json:"avatar_url"`
	EloRating int       `json:"elo_rating"`
	CreatedAt time.Time `json:"created_at"`
}

func NewCurrentUserResponse(user *domain.User) CurrentUserResponse {
	return CurrentUserResponse{
		ID:        user.ID,
		Username:  user.Username,
		Email:     user.Email,
		AvatarURL: user.AvatarURL,
		EloRating: user.EloRating,
		CreatedAt: user.CreatedAt,
	}
}
