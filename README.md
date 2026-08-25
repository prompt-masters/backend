# Prompt Masters Backend API

Backend API for Prompt Masters, built with Go.

## Requirements

- Go 1.26.5+
- Docker & Docker Compose

## Getting Started

Prepare environment variables:

```sh
cp .env.example .env
```

Start the database:

```sh
make up
```

Run the server:

```sh
go mod tidy
go run ./cmd/server
```

## Database migrations

Migrations live in `migrations/` and are applied with the
[golang-migrate](https://github.com/golang-migrate/migrate) CLI:

```sh
make migrate-up     # apply all pending migrations
make migrate-down   # roll back the most recent one
```

## Tests

```sh
make test
```

Repository tests need a real Postgres and are skipped unless
`TEST_DATABASE_URL` is set, so the suite runs without a database. To
include them, create the test database once and point the variable at it:

```sh
createdb prompters_test_db
migrate -path ./migrations -database "$TEST_DATABASE_URL" up
```

These tests truncate the `users` table, so point `TEST_DATABASE_URL` at a
database you do not mind losing.
