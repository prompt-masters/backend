package api

import (
	"log"
	"net/http"

	"github.com/prompt-masters/backend/internal/api/handler"
	"github.com/prompt-masters/backend/internal/api/middleware"
	"github.com/prompt-masters/backend/internal/service"
)

type Server struct {
	auth       *handler.AuthHandler
	challenges *handler.ChallengeHandler
	games      *handler.GameHandler
	logger     *log.Logger
	jwtSecret  string
}

func NewServer(
	authService *service.AuthService,
	challengeService *service.ChallengeService,
	gameService *service.GameService,
	logger *log.Logger,
	jwtSecret string,
) *Server {
	return &Server{
		auth:       handler.NewAuthHandler(authService, logger),
		challenges: handler.NewChallengeHandler(challengeService, logger),
		games:      handler.NewGameHandler(gameService, logger),
		logger:     logger,
		jwtSecret:  jwtSecret,
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
	mux.HandleFunc("GET /api/v1/challenges", s.challenges.List)
	mux.HandleFunc("GET /api/v1/challenges/categories", s.challenges.Categories)
	mux.HandleFunc("GET /api/v1/challenges/{id}", s.challenges.Get)

	// Protected
	protected := func(method, path string, handler http.HandlerFunc) {
		mux.Handle(method+" "+path, middleware.RequireAuth(s.jwtSecret, handler))
	}
	protected("GET", "/api/v1/auth/me", s.auth.Me)
	protected("GET", "/api/v1/auth/profile", s.auth.Profile)
	protected("PUT", "/api/v1/auth/profile", s.auth.UpdateProfile)
	protected("GET", "/api/v1/games", s.games.List)
	protected("POST", "/api/v1/games", s.games.Create)
	protected("GET", "/api/v1/games/{id}", s.games.Get)
	protected("PUT", "/api/v1/games/{id}/settings", s.games.UpdateSettings)
	protected("POST", "/api/v1/games/{id}/join", s.games.Join)
	protected("POST", "/api/v1/games/{id}/leave", s.games.Leave)
	protected("POST", "/api/v1/games/{id}/start", s.games.Start)
	protected("DELETE", "/api/v1/games/{id}", s.games.Cancel)

	return mux
}
