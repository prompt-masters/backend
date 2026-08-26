# Prompt Masters Backend API

Backend API for Prompt Masters, built with Go.

## Requirements

- Docker & Docker Compose
- Go 1.26.5+ (only to run or test outside Docker)

## Run with Docker

Brings up Postgres, applies migrations, and starts the API. No Go toolchain
needed.

```sh
cp .env.example .env
make up
make logs
```

The API is on `http://localhost:8080`. If 8080 or 5432 are already taken on
your machine, set `APP_PORT` and `POSTGRES_PORT` in `.env`.

`make down` stops everything.

The app image is built from `Dockerfile`: a static binary on distroless, so
the runtime has no shell and nothing to pivot with. `.env` is excluded by
`.dockerignore` and never enters the image — compose passes configuration in
at runtime.

## Getting Started (without Docker)

Prepare environment variables:

```sh
cp .env.example .env
```

Start just the database:

```sh
docker compose up -d postgres
make migrate-up
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

## API

### `POST /api/v1/auth/register`

```json
{ "username": "alice", "email": "alice@example.com", "password": "hunter2secret" }
```

`201 Created`:

```json
{ "user_id": 1, "username": "alice", "email": "alice@example.com", "elo_rating": 1200 }
```

The account starts unverified and rated 1200. A verification link is emailed
in the background, so a slow or unreachable SMTP server never delays or fails
a registration that already succeeded — check the server log if mail is
misconfigured.

### `GET /api/v1/auth/verify-email?token=...`

Redeems the emailed link and marks the address verified. Tokens last 24 hours
and can be spent once.

### Errors

Every failure has the same shape, with `fields` added for validation errors:

```json
{ "error_code": "VALIDATION_ERROR", "message": "The submitted values are not valid.",
  "fields": { "username": "must be at least 3 characters" } }
```

| Status | `error_code` | Meaning |
| --- | --- | --- |
| 400 | `MALFORMED_REQUEST` | Unparseable body, unknown field, oversized body, or a non-JSON `Content-Type` |
| 422 | `VALIDATION_ERROR` | Well-formed body, invalid values |
| 409 | `EMAIL_ALREADY_EXISTS` | That email is registered |
| 409 | `USERNAME_ALREADY_EXISTS` | That username is taken (compared case-insensitively) |
| 400 | `INVALID_TOKEN` | Verification token missing, unknown, or already used |
| 410 | `TOKEN_EXPIRED` | Verification token older than 24 hours |
| 500 | `INTERNAL_ERROR` | Anything else; the cause is logged, never returned |

### Validation rules

- **username** — required, 3–32 characters, letters, digits and underscores
- **email** — required, at most 254 characters, a bare address with a dotted domain
- **password** — required, at least 8 characters, at most 72 bytes (bcrypt's limit)

## Email

Set `MAIL_HOST`, `MAIL_PORT`, `MAIL_USERNAME`, `MAIL_PASSWORD` and
`MAIL_FROM_EMAIL` to send through an SMTP server; on port 587 STARTTLS is
negotiated automatically. For Gmail, `MAIL_PASSWORD` must be an
[app password](https://support.google.com/accounts/answer/185833), not the
account password.

Leave `MAIL_HOST` empty and verification links are written to the server log
instead of emailed, which is usually what you want locally.

`APP_BASE_URL` is the public origin used to build those links.
