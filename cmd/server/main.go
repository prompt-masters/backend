package main

import (
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"

	"github.com/prompt-masters/backend/internal/api"
	"github.com/prompt-masters/backend/internal/auth"
	"github.com/prompt-masters/backend/internal/mail"
)

func main() {
	// A .env file is a development convenience. In production the values come
	// from the real environment, so its absence is not an error.
	if err := godotenv.Load(); err != nil {
		log.Printf("no .env file loaded (%v); using the environment", err)
	}

	logger := log.New(os.Stderr, "", log.LstdFlags|log.Lmsgprefix)

	host := os.Getenv("HOST")
	port := envOr("PORT", "8080")
	addr := net.JoinHostPort(host, port)

	db, err := openDatabase(os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	sender := mail.NewSenderFromConfig(mailConfig(), os.Stderr)
	if _, ok := sender.(*mail.LogSender); ok {
		logger.Printf("warning: SMTP is not configured; verification links will be written to this log instead of emailed")
	}

	baseURL := envOr("APP_BASE_URL", "http://localhost:"+port)
	service := auth.NewService(auth.NewPostgresRepository(db), auth.DefaultBcryptCost)

	mux := http.NewServeMux()
	api.NewAuthHandler(service, sender, baseURL, logger).RegisterRoutes(mux)

	s := http.Server{
		Addr:    addr,
		Handler: mux,
		// Without these a slow or idle client can hold a connection open
		// indefinitely. WriteTimeout is generous enough to cover a bcrypt
		// hash on a loaded machine.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	logger.Printf("running server on %s", s.Addr)
	log.Fatal(s.ListenAndServe())
}

func openDatabase(url string) (*sql.DB, error) {
	if url == "" {
		return nil, errors.New("DATABASE_URL is not set")
	}

	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func mailConfig() mail.Config {
	return mail.Config{
		Host:      os.Getenv("MAIL_HOST"),
		Port:      envInt("MAIL_PORT", 587),
		Username:  os.Getenv("MAIL_USERNAME"),
		Password:  os.Getenv("MAIL_PASSWORD"),
		FromEmail: os.Getenv("MAIL_FROM_EMAIL"),
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}
