package config

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Host        string
	Port        string
	DatabaseURL string
	JWTSecret   string
	AppBaseURL  string
	Mail        MailConfig
}

type MailConfig struct {
	Host      string
	Port      int
	Username  string
	Password  string
	FromEmail string
}

func Load() (*Config, error) {
	_ = godotenv.Load()

	cfg := &Config{
		Host:        os.Getenv("HOST"),
		Port:        os.Getenv("PORT"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
		JWTSecret:   os.Getenv("JWT_SECRET"),
		AppBaseURL:  os.Getenv("APP_BASE_URL"),
		Mail: MailConfig{
			Host:      os.Getenv("MAIL_HOST"),
			Port:      envInt("MAIL_PORT", 587),
			Username:  os.Getenv("MAIL_USERNAME"),
			Password:  os.Getenv("MAIL_PASSWORD"),
			FromEmail: os.Getenv("MAIL_FROM_EMAIL"),
		},
	}

	if cfg.Port == "" {
		cfg.Port = "8080"
	}
	if cfg.AppBaseURL == "" {
		cfg.AppBaseURL = "http://localhost:" + cfg.Port
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
