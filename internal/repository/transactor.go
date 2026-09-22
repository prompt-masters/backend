package repository

import "context"

// TxRepositories are repositories bound to one database transaction.
type TxRepositories struct {
	Games      GameRepository
	Challenges ChallengeRepository
}

// Transactor runs fn in a single database transaction, handing it repositories
// bound to that transaction. The transaction commits when fn returns nil and
// rolls back otherwise.
//
// While fn holds row locks it must only use these repositories: reaching for
// another connection could wait on a pool that is exhausted by transactions
// queued behind the very lock fn holds.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(repos TxRepositories) error) error
}
