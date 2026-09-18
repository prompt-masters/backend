package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prompt-masters/backend/internal/api"
	"github.com/prompt-masters/backend/internal/api/handler"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/redisclient"
	"github.com/prompt-masters/backend/internal/repository/postgres"
	"github.com/prompt-masters/backend/internal/repository/redisstore"
	"github.com/prompt-masters/backend/internal/service"
	"github.com/prompt-masters/backend/internal/ws"
)

const shutdownTimeout = 15 * time.Second

func main() {
	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)
	if err := run(logger); err != nil {
		logger.Fatal(err)
	}
}

func run(logger *log.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		return err
	}

	// Redis only holds live game state, so the API starts without it: live
	// state endpoints answer 503 until the client reconnects on its own.
	redisClient, err := redisclient.New(cfg.Redis)
	if err != nil {
		return err
	}
	defer func() {
		if err := redisClient.Close(); err != nil {
			logger.Printf("closing redis client: %v", err)
		}
	}()
	if err := redisclient.Ping(ctx, redisClient, cfg.Redis.DialTimeout); err != nil {
		logger.Printf("redis unavailable at %s, starting in degraded mode: %v", cfg.Redis.Addr, err)
	} else {
		logger.Printf("redis connected at %s (db %d, tls %t)", cfg.Redis.Addr, cfg.Redis.DB, cfg.Redis.TLS)
	}

	queries := db.New(pool)
	userRepo := postgres.NewUserRepository(queries)
	tokenRepo := postgres.NewVerificationTokenRepository(queries)
	refreshTokenRepo := postgres.NewRefreshTokenRepository(queries)
	challengeRepo := postgres.NewChallengeRepository(queries)
	transactor := postgres.NewTransactor(pool)
	sender := mail.NewSenderFromConfig(cfg.Mail, os.Stderr)

	authService := service.NewAuthService(userRepo, tokenRepo, refreshTokenRepo, sender, *cfg)
	challengeService := service.NewChallengeService(challengeRepo)
	gameStateStore, err := redisstore.New(redisClient, redisstore.Options{
		KeyPrefix: cfg.Redis.KeyPrefix,
		ActiveTTL: cfg.Redis.ActiveGameTTL,
		EndedTTL:  cfg.Redis.EndedGameTTL,
		OpTimeout: cfg.Redis.OpTimeout,
	})
	if err != nil {
		return err
	}
	liveStateService := service.NewLiveStateService(gameStateStore, transactor, logger, time.Now)

	hub, err := ws.NewHub(cfg.WS.Hub, nil, logger)
	if err != nil {
		return err
	}
	// The router needs the hub to broadcast, and the hub needs the router to
	// deliver client messages, so it is attached once both exist.
	hub.SetRouter(service.NewWSRouter(liveStateService, hub, logger))
	gameEvents := service.NewGameEvents(hub, logger)
	gameService := service.NewGameService(transactor, service.MathRandom{}, liveStateService, gameEvents)
	websocketHandler := handler.NewWebSocketHandler(hub, liveStateService, cfg.JWTSecret, cfg.WS.AllowedOrigins, logger)

	healthChecks := []handler.HealthCheck{
		{Name: "postgres", Critical: true, Check: pool.Ping},
		{Name: "redis", Check: func(ctx context.Context) error { return redisClient.Ping(ctx).Err() }},
	}
	server := api.NewServer(authService, challengeService, gameService, liveStateService, websocketHandler, healthChecks, logger, cfg.JWTSecret)

	s := &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           server.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Printf("running server on %s", s.Addr)
		serveErr <- s.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		logger.Printf("shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Stop accepting requests first, then close the live sockets.
	err = s.Shutdown(shutdownCtx)
	if hubErr := hub.Shutdown(shutdownCtx); hubErr != nil {
		logger.Printf("ws op=shutdown error=%q", hubErr)
	}
	return err
}
