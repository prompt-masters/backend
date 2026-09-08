package config

import (
	"fmt"
	"os"
	"time"

	"github.com/joho/godotenv"
	"github.com/prompt-masters/backend/internal/mail"
)

const (
	defaultPort            = "8080"
	defaultJWTTTL          = 15 * time.Minute
	defaultRefreshTokenTTL = 30 * 24 * time.Hour
	defaultMailPort        = 587
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
