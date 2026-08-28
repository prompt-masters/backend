package api

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/handler"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/service"
)

type Server struct {
	auth      *handler.AuthHandler
	logger    *log.Logger
	jwtSecret string
}

func NewServer(
	authService *service.AuthService,
	logger *log.Logger,
	jwtSecret string,
) *Server {
	return &Server{
		auth:      handler.NewAuthHandler(authService, logger),
		logger:    logger,
		jwtSecret: jwtSecret,
	}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Public
	mux.HandleFunc("POST /api/v1/auth/register", s.auth.Register)
	mux.HandleFunc("POST /api/v1/auth/login", s.auth.Login)
	mux.HandleFunc("GET /api/v1/auth/verify-email", s.auth.VerifyEmail)

	// Protected
	mux.Handle("GET /api/v1/auth/me",
		middleware.RequireAuth(s.jwtSecret, http.HandlerFunc(s.auth.Me)),
	)

	return mux
}
