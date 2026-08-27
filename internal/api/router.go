package api

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/handler"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/service"
)

type Dependencies struct {
	AuthService *service.AuthService
	Logger      *log.Logger
	JWTSecret   string
}

func NewRouter(deps Dependencies) *http.ServeMux {
	auth := handler.NewAuthHandler(deps.AuthService, deps.Logger)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/auth/register", auth.Register)
	mux.HandleFunc("POST /api/v1/auth/login", auth.Login)
	mux.HandleFunc("GET /api/v1/auth/verify-email", auth.VerifyEmail)
	mux.Handle("GET /api/v1/auth/me", middleware.RequireAuth(deps.JWTSecret, http.HandlerFunc(auth.Me)))
	return mux
}
