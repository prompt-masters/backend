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
	mux.HandleFunc("POST /api/v1/auth/refresh", s.auth.Refresh)
	mux.HandleFunc("POST /api/v1/auth/logout", s.auth.Logout)
	mux.HandleFunc("GET /api/v1/auth/verify-email", s.auth.VerifyEmail)

	// Protected
	protected := func(method, path string, handler http.HandlerFunc) {
		mux.Handle(method+" "+path, middleware.RequireAuth(s.jwtSecret, handler))
	}
	protected("GET", "/api/v1/auth/me", s.auth.Me)
	protected("GET", "/api/v1/auth/profile", s.auth.Profile)
	protected("PUT", "/api/v1/auth/profile", s.auth.UpdateProfile)

	return mux
}
