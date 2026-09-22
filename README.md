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

# 4. Load dev challenges (optional, safe to re-run)
make seed

# 5. Start server
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
| `make seed` | Load dev challenge data (idempotent, safe to re-run) |
| `make migrate-create <name>` | Create a new migration file |
| `make sqlc` | Regenerate sqlc code |

## Development

See [DEV_GUIDE.md](DEV_GUIDE.md) for the full feature development workflow.

**Quick flow:** domain → migration → sqlc queries → `make sqlc` → repository → service → api handler → wire in main.go

## Tests

```sh
make test
```

Repository tests run against `TEST_DATABASE_URL`. Each test migrates a
throwaway schema and drops it afterwards, so existing tables are untouched.
They are skipped when `TEST_DATABASE_URL` is unset. The API integration tests
in `internal/api` use the same database setup.

Run with the race detector to cover the concurrency tests properly:

```sh
go test -race ./...
```

## API

### `POST /api/v1/auth/register`

```json
{ "username": "alice", "email": "alice@example.com", "password": "hunter2secret" }
```

### `GET /api/v1/auth/verify-email?token=...`

Redeems the emailed link and marks the address verified. Tokens last 24 hours
and can be spent once.

### `GET /api/v1/challenges`

Lists challenges. Optional filters, usable alone or together:

- `category`: one of `creative_writing`, `coding`, `data_extraction`, `summarization`, `reasoning`
- `difficulty`: one of `easy`, `medium`, `hard`

Unknown values return 400 with field details.

### `GET /api/v1/challenges/{id}`

Returns one challenge with its `constraints` and `judge_criteria`. Unknown or
malformed IDs return 404.

### `GET /api/v1/challenges/categories`

Returns the list of available categories.

### Games

All game routes require `Authorization: Bearer <access token>`. `{id}` is the
game's UUID or the 6-digit room code of a waiting or in-progress game, so
players can join straight from a shared code: `POST /api/v1/games/004213/join`.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/api/v1/games?status=waiting` | 200 | `status` defaults to `waiting`; newest first, up to 100 |
| `POST` | `/api/v1/games` | 201 | Creator becomes host and first player |
| `GET` | `/api/v1/games/{id}` | 200 | Settings and players |
| `PUT` | `/api/v1/games/{id}/settings` | 200 | Host only, while `waiting`; replaces all settings |
| `POST` | `/api/v1/games/{id}/join` | 200 | |
| `POST` | `/api/v1/games/{id}/leave` | 204 | Only while `waiting` |
| `POST` | `/api/v1/games/{id}/start` | 200 | Host only; `waiting` → `in_progress` |
| `DELETE` | `/api/v1/games/{id}` | 204 | Host only; sets status `cancelled` |

Settings body, required on create and update:

```json
{ "rounds": 5, "time_per_round": 90, "difficulty": "medium", "category": "coding",
  "ai_model": "claude-sonnet-5", "max_players": 4 }
```

- `rounds`: 3, 5 or 7
- `time_per_round` (seconds): 60, 90 or 120
- `difficulty` and `category`: the challenge values above
- `ai_model`: `claude-opus-5`, `claude-sonnet-5` or `claude-haiku-4-5`
- `max_players`: 2 to 6, and not below the current player count

Rules:

- **Host leaves:** hosting passes to the player who joined earliest. When no
  players remain, the game is cancelled.
- **Start:** needs at least 2 players and at least one challenge for the game's
  category and difficulty.
- **Cancel:** allowed while `waiting` or `in_progress`.
- **Room codes:** unique among active games and reused once a game finishes or
  is cancelled.
- **Concurrency:** every change locks the game row, so concurrent joins cannot
  overfill a room and nothing lands after a game starts.

Status codes: 400 invalid input, 401 unauthenticated, 403 not the host,
404 unknown game, 409 state conflict (already started, room full, already
joined, not a player, not enough players, no eligible challenge).

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
