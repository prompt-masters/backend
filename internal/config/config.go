package config

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	"github.com/prompt-masters/backend/internal/mail"
	"github.com/prompt-masters/backend/internal/redisclient"
)

const (
	defaultPort            = "8080"
	defaultJWTTTL          = 15 * time.Minute
	defaultRefreshTokenTTL = 30 * 24 * time.Hour
	defaultMailPort        = 587

	defaultRedisAddr         = "localhost:6379"
	defaultRedisPoolSize     = 20
	defaultRedisDialTimeout  = 2 * time.Second
	defaultRedisReadTimeout  = time.Second
	defaultRedisWriteTimeout = time.Second
	defaultRedisOpTimeout    = 2 * time.Second
	defaultRedisKeyPrefix    = "promptgame"
	defaultActiveGameTTL     = 2 * time.Hour
	defaultEndedGameTTL      = 15 * time.Minute
)

type Config struct {
	Host            string
	Port            string
	DatabaseURL     string
	JWTSecret       string
	JWTTTL          time.Duration
	RefreshTokenTTL time.Duration
	AppBaseURL      string
	Mail            mail.Config
	Redis           redisclient.Config
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Host:        os.Getenv("HOST"),
		Port:        os.Getenv("PORT"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		AppBaseURL:  os.Getenv("APP_BASE_URL"),
		Mail: mail.Config{
			Host:      os.Getenv("MAIL_HOST"),
			Port:      envInt("MAIL_PORT", defaultMailPort),
			Username:  os.Getenv("MAIL_USERNAME"),
			Password:  os.Getenv("MAIL_PASSWORD"),
			FromEmail: os.Getenv("MAIL_FROM_EMAIL"),
		},
		Redis: redisclient.Config{
			Addr:          envString("REDIS_ADDR", defaultRedisAddr),
			Username:      os.Getenv("REDIS_USERNAME"),
			Password:      os.Getenv("REDIS_PASSWORD"),
			DB:            envInt("REDIS_DB", 0),
			TLS:           envBool("REDIS_TLS", false),
			PoolSize:      envInt("REDIS_POOL_SIZE", defaultRedisPoolSize),
			DialTimeout:   envDuration("REDIS_DIAL_TIMEOUT", defaultRedisDialTimeout),
			ReadTimeout:   envDuration("REDIS_READ_TIMEOUT", defaultRedisReadTimeout),
			WriteTimeout:  envDuration("REDIS_WRITE_TIMEOUT", defaultRedisWriteTimeout),
			OpTimeout:     envDuration("REDIS_OP_TIMEOUT", defaultRedisOpTimeout),
			KeyPrefix:     envString("REDIS_KEY_PREFIX", defaultRedisKeyPrefix),
			ActiveGameTTL: envDuration("GAME_STATE_ACTIVE_TTL", defaultActiveGameTTL),
			EndedGameTTL:  envDuration("GAME_STATE_ENDED_TTL", defaultEndedGameTTL),
		},
	}

	if cfg.Port == "" {
		cfg.Port = defaultPort
	}
	if cfg.AppBaseURL == "" {
		cfg.AppBaseURL = "http://localhost:" + cfg.Port
	}

	jwtTTL, err := time.ParseDuration(os.Getenv("JWT_TTL"))
	if err != nil {
		cfg.JWTTTL = defaultJWTTTL
	} else {
		cfg.JWTTTL = jwtTTL
	}

	refreshTTL, err := time.ParseDuration(os.Getenv("REFRESH_TOKEN_TTL"))
	if err != nil {
		cfg.RefreshTokenTTL = defaultRefreshTokenTTL
	} else {
		cfg.RefreshTokenTTL = refreshTTL
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if cfg.JWTSecret == "" {
		return nil, fmt.Errorf("JWT_SECRET is required")
	}
	if err := cfg.Redis.Validate(); err != nil {
		return nil, fmt.Errorf("invalid Redis config: %w", err)
	}

	return cfg, nil
}

func envInt(key string, fallback int) int {
	v := 0
	fmt.Sscanf(os.Getenv(key), "%d", &v)
	if v == 0 {
		return fallback
	}
	return v
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v, err := time.ParseDuration(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
