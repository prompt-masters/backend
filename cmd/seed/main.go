// Command seed loads development challenge data. It is idempotent: challenges
// are upserted by slug, so it is safe to run repeatedly.
package main

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prompt-masters/backend/internal/config"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/repository/postgres"
	"github.com/prompt-masters/backend/internal/seed"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("Unable to create connection pool: %v", err)
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		log.Fatalf("Unable to begin transaction: %v", err)
	}
	defer tx.Rollback(ctx)

	n, err := seed.Run(ctx, postgres.NewChallengeRepository(db.New(tx)))
	if err != nil {
		log.Fatalf("Seeding challenges failed: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		log.Fatalf("Unable to commit seed: %v", err)
	}

	log.Printf("seeded %d challenges", n)
}
