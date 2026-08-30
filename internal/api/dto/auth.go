package dto

import (
	"github.com/prompt-masters/backend/internal/domain"
)

type RegisterRequest struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type LoginResponse struct {
	User  *domain.User `json:"user"`
	Token string       `json:"token"`
}
