package main

import (
	"database/sql"
	"log"
	"net"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
)

func main() {
	// loading env file
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	host := os.Getenv("HOST")
	dbURL := os.Getenv("DATABASE_URL")

	db, err := sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatalf("Invalid connection string format: %v", err)
	}
	defer db.Close()

	err = db.Ping()
	if err != nil {
		log.Fatalf("Database connection failed: %v", err)
	}

	mux := http.NewServeMux()

	s := http.Server{
		Addr:    net.JoinHostPort(host, port),
		Handler: mux,
	}

	log.Printf("Running server on %s", s.Addr)
	log.Fatal(s.ListenAndServe())
}
