# Prompt Masters Backend API

Backend API for Prompt Masters, built with Go.

## Project Structure

```
backend/
├── cmd/server/main.go                     # Entry point, wires everything together
│
├── internal/
│   ├── api/                               # HTTP / Transport layer
│   │   ├── response.go                    # JSON response helpers
│   │   └── middleware/                    # Auth, logging, CORS, etc.
│   │
│   ├── config/config.go                   # Env loading + validation
│   │
│   ├── db/
│   │   ├── migrations/                    # Goose SQL migrations
│   │   ├── queries/                       # SQLC query files
│   │   ├── models.go                      # SQLC generated models
│   │   ├── db.go                          # SQLC generated DB interface
│   │   ├── querier.go                     # SQLC generated Querier interface
│   │   └── *.sql.go                       # SQLC generated query code
│   │
│   ├── domain/                            # Pure business entities
│   │   ├── user.go
│   │   └── verification_token.go
│   │
│   ├── repository/                        # Data access interfaces
│   │   ├── user.go
│   │   ├── verification_token.go
│   │   └── postgres/                      # Postgres implementations
│   │       ├── helpers.go                 # UUID converters
│   │       ├── user.go
│   │       └── verification_token.go
│   │
│   ├── service/                           # Business logic / use cases
│   │
│   └── util/                              # Shared helpers
│       └── password.go                    # Argon2id hash + verify
│
├── sqlc.yaml
├── Makefile
├── docker-compose.yaml
├── Dockerfile
├── DEV_GUIDE.md                           # Feature development workflow
└── .env.example
```

## Tech Stack

- **Go** 1.26.5+
- **PostgreSQL** 16 (via Docker)
- **[goose](https://github.com/pressly/goose)** — database migrations
- **[sqlc](https://sqlc.dev)** — type-safe SQL query generation
- **[pgx/v5](https://github.com/jackc/pgx)** — PostgreSQL driver
- **argon2id** — password hashing
- **google/uuid** — UUID generation

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

## Getting Started (without Docker)

**Requirements:** Go 1.26.5+, Docker, Docker Compose

```sh
# 1. Setup environment
cp .env.example .env

# 2. Start postgres
make up

# 3. Run migrations
make migrate-up

# 4. Start server
make run
```

## Make Commands

| Command | Description |
|---|---|
| `make up` | Start all containers |
| `make down` | Stop all containers |
| `make logs` | Tail container logs |
| `make run` | Build and run server locally |
| `make fmt` | Format Go code |
| `make test` | Run tests |
| `make migrate-up` | Apply all migrations |
| `make migrate-down` | Rollback last migration |
| `make migrate-create <name>` | Create a new migration file |
| `make sqlc` | Regenerate sqlc code |

## Development

See [DEV_GUIDE.md](DEV_GUIDE.md) for the full feature development workflow.

**Quick flow:** domain → migration → sqlc queries → `make sqlc` → repository → service → api handler → wire in main.go

## Tests

```sh
make test
```

## API

### `POST /api/v1/auth/register`

```json
{ "username": "alice", "email": "alice@example.com", "password": "hunter2secret" }
```

### `GET /api/v1/auth/verify-email?token=...`

Redeems the emailed link and marks the address verified. Tokens last 24 hours
and can be spent once.

### Errors

Every failure has the same shape:

```json
{ "error_code": "VALIDATION_ERROR", "message": "The submitted values are not valid.",
  "fields": { "username": "must be at least 3 characters" } }
```

| Status | `error_code` | Meaning |
| --- | --- | --- |
| 400 | `MALFORMED_REQUEST` | Unparseable body or unknown field |
| 422 | `VALIDATION_ERROR` | Well-formed body, invalid values |
| 409 | `EMAIL_ALREADY_EXISTS` | That email is registered |
| 409 | `USERNAME_ALREADY_EXISTS` | That username is taken |
| 400 | `INVALID_TOKEN` | Verification token missing or unknown |
| 410 | `TOKEN_EXPIRED` | Verification token older than 24 hours |
| 500 | `INTERNAL_ERROR` | Anything else; cause is logged, never returned |

## Email

Set `MAIL_HOST`, `MAIL_PORT`, `MAIL_USERNAME`, `MAIL_PASSWORD` and
`MAIL_FROM_EMAIL` to send through SMTP; on port 587 STARTTLS is
negotiated automatically.

Leave `MAIL_HOST` empty and verification links are written to the server log
instead of emailed, which is usually what you want locally.

`APP_BASE_URL` is the public origin used to build those links.
