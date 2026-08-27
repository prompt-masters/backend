package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prompt-masters/backend/internal/api"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/repository/postgres"
	"github.com/prompt-masters/backend/internal/service"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Unable to create connection pool: %v", err)
	}
	defer pool.Close()

	if err := pool.Ping(context.Background()); err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)

	queries := db.New(pool)
	userRepo := postgres.NewUserRepository(queries)
	tokenRepo := postgres.NewVerificationTokenRepository(queries)
	sender := mail.NewSenderFromConfig(cfg.Mail, os.Stderr)

	authService := service.NewAuthService(userRepo, tokenRepo, sender, service.Config{
		JWTSecret:  cfg.JWTSecret,
		JWTTTL:     cfg.JWTTTL,
		AppBaseURL: cfg.AppBaseURL,
	})

	mux := api.NewRouter(api.Dependencies{
		AuthService: authService,
		Logger:      logger,
		JWTSecret:   cfg.JWTSecret,
	})

	s := http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Printf("running server on %s", s.Addr)
	log.Fatal(s.ListenAndServe())
}
