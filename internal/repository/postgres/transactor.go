package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/prompt-masters/backend/internal/db"
	"github.com/prompt-masters/backend/internal/repository"
)

// TxBeginner starts transactions; *pgxpool.Pool satisfies it.
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

type Transactor struct {
	pool TxBeginner
}

func NewTransactor(pool TxBeginner) *Transactor {
	return &Transactor{pool: pool}
}

func (t *Transactor) WithinTx(ctx context.Context, fn func(repos repository.TxRepositories) error) error {
	return pgx.BeginFunc(ctx, t.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)
		return fn(repository.TxRepositories{
			Games:      NewGameRepository(queries),
			Challenges: NewChallengeRepository(queries),
		})
	})
}
