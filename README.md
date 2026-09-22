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

## Setup

**Requirements:** Docker and Docker Compose. Go 1.26.5+ only if you want to
run the API or the tests outside a container.

### 1. Environment

```sh
cp .env.example .env
```

Then edit `.env`:

| Variable | Why |
| --- | --- |
| `REDIS_PASSWORD` | **Required.** Docker Compose refuses to start Redis without it, and the API container uses the same value. Pick anything for local dev. |
| `JWT_SECRET` | Signs access tokens. Change it from the example value. |
| `APP_PORT`, `POSTGRES_PORT`, `REDIS_PORT` | Change only if 8080, 5432 or 6379 are already taken on your machine. |
| `TEST_DATABASE_URL`, `TEST_REDIS_*` | Needed to run the tests; see [Tests](#tests). |

### 2. Everything in Docker

```sh
make up            # postgres + redis + the API
make migrate-up    # create the schema (goose)
make seed          # load dev challenges, safe to re-run
make logs
```

The API is then on `http://localhost:${APP_PORT:-8080}`; check
`curl localhost:8080/health`. `make down` stops everything.

### 3. Or run the API locally against containers

```sh
docker compose up -d postgres redis
make migrate-up
make seed
make run
```

`make migrate-up` and `make seed` use `DATABASE_URL` from `.env`, which points
at the published Postgres port, so they work either way.

**Requirements for this path:** Go 1.26.5+, plus
[goose](https://github.com/pressly/goose) for `make migrate-up`. Without
goose installed:

```sh
go run github.com/pressly/goose/v3/cmd/goose@latest \
  -dir internal/db/migrations postgres "$DATABASE_URL" up
```

### Resetting a database created before migrations existed

If `make migrate-up` fails with `relation "users" already exists`, the
database predates goose. Recreate it (this deletes its data):

```sh
# local postgres
dropdb prompters_db && createdb prompters_db
# or the compose volume
make down && docker volume rm backend_postgres_data && make up
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

Redis tests run against `TEST_REDIS_ADDR` (with `TEST_REDIS_PASSWORD`, and
`TEST_REDIS_DB`, default 15). Each test uses its own key prefix and deletes
its keys afterwards; they are skipped when `TEST_REDIS_ADDR` is unset.

```sh
docker compose up -d postgres redis
make test          # or: go test -race ./...
```

`make test` exports `.env`, so the `TEST_*` variables set there are picked up.
Database tests create a throwaway schema per test and drop it afterwards, so
they leave the database they connect to untouched.

Set `TEST_REDIS_CLUSTER_ADDRS` (comma-separated nodes, optionally with
`TEST_REDIS_CLUSTER_PASSWORD`) to also run the Redis Cluster test, which
checks that a game's keys share one slot. It is skipped when unset.

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

### Live game state

All routes require authentication. The acting user always comes from the
access token; no route accepts a user ID.

| Method | Path | Success | Notes |
| --- | --- | --- | --- |
| `GET` | `/api/v1/games/{id}/live` | 200 | Reconnect snapshot: lobby, players with `is_ready` and `points`, `round` with server-computed `remaining_ms`, your own `draft` |
| `PUT` | `/api/v1/games/{id}/ready` | 204 | Mark yourself ready (`waiting` games) |
| `DELETE` | `/api/v1/games/{id}/ready` | 204 | Unmark |
| `GET` | `/api/v1/games/{id}/draft` | 200 | Your own draft; 404 if you have none |
| `PUT` | `/api/v1/games/{id}/draft` | 200 | `{"content": "..."}`, up to 8 KB, `in_progress` games |

Only players of the game get live state (403 otherwise). A player can never
read another player's draft. 503 means Redis is unavailable; retry later.

### `GET /health`

`{"status": "ok" | "degraded" | "unavailable", "checks": {"postgres": "up", "redis": "up"}}`.
Returns 503 only when Postgres is down.

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

## Redis (live game state)

PostgreSQL is the source of truth for games, settings and players. Redis
holds fast-changing live state: the lobby snapshot, roster, readiness, the
current round, drafts and helper points.

### Running locally

`docker compose up -d redis` starts Redis 7 on `127.0.0.1:${REDIS_PORT:-6379}`.
It requires `REDIS_PASSWORD` in `.env`; compose refuses to start without it.
The port is bound to localhost only and persistence is off, since everything
in it can be lost or rebuilt.

### Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `REDIS_ADDR` | `localhost:6379` | Host and port |
| `REDIS_USERNAME` / `REDIS_PASSWORD` | empty | ACL credentials |
| `REDIS_DB` | `0` | Database index |
| `REDIS_TLS` | `false` | TLS (1.2+, verified against the system roots) |
| `REDIS_TLS_CA_FILE` | empty | PEM bundle to verify the server against instead of the system roots (private CA) |
| `REDIS_TLS_SERVER_NAME` | empty | Overrides the name checked against the server certificate |
| `REDIS_POOL_SIZE` | `20` | Connection pool size |
| `REDIS_DIAL_TIMEOUT` / `REDIS_READ_TIMEOUT` / `REDIS_WRITE_TIMEOUT` | `2s` / `1s` / `1s` | Socket timeouts |
| `REDIS_OP_TIMEOUT` | `2s` | Upper bound for each live-state operation |
| `REDIS_KEY_PREFIX` | `promptgame` | Namespace for every key |
| `GAME_STATE_ACTIVE_TTL` | `2h` | Sliding expiry for waiting and in-progress games |
| `GAME_STATE_ENDED_TTL` | `15m` | Expiry once a game is finished or cancelled |

### Keys and data types

All keys are built in `internal/repository/redisstore/keys.go`:

| Key | Type | Contents |
| --- | --- | --- |
| `promptgame:game:{gameId}:state` | Hash | `status`, `host_id`, `room_code`, `settings` (JSON), `updated_at_ms` |
| `promptgame:game:{gameId}:players` | Hash | user ID → JSON `{user_id, username, joined_at_ms}` |
| `promptgame:game:{gameId}:ready` | Set | ready user IDs |
| `promptgame:game:{gameId}:round` | Hash | `number`, `challenge_id`, `started_at_ms`, `deadline_ms` (server time) |
| `promptgame:game:{gameId}:draft:{userId}` | String | JSON `{content, round_number, updated_at_ms}` |
| `promptgame:game:{gameId}:points` | Hash | user ID → integer (`HINCRBY`) |

The braces around the game ID are a Redis Cluster hash tag, so a game's keys
share one slot. IDs are UUIDs and are checked before a key is built.

- **Atomic writes:** every write is a single Lua script that makes the change
  and then resets the expiry of *all* the game's keys, drafts included. No
  key ever exists without a TTL.
- **Ready, draft and points writes:** the script checks the user is on the
  roster before writing.
- **Reconnect snapshot:** read with `MULTI/EXEC`, so every part comes from the
  same moment.

### When Redis is unavailable

- **At startup:** the API logs a warning and starts anyway (degraded mode);
  go-redis reconnects on its own.
- **`/health`:** reports `redis: down` with status `degraded` (still 200).
  Postgres being down gives 503.
- **Live-state endpoints:** answer `503` with `Retry-After: 5` and a JSON
  error. The failure is logged as
  `live_state op=... game_id=... user_id=... unavailable=true error=...`.
- **Logs never contain** draft content or credentials.
- **Lobby endpoints** keep working. Their changes are mirrored into Redis after
  the Postgres commit, best effort: a failed mirror is logged and does not fail
  the request.

### Reconnecting and rebuilding

`GET /api/v1/games/{id}/live` returns the full live state for a player.

- **Membership** is checked against the Redis roster. If Redis refuses a user,
  Postgres decides, and a stale roster is re-synced.
- **When Redis has no state** for the game (expired, evicted, flushed), the
  lobby and roster are rebuilt from Postgres and written back for active
  games. Readiness, the current round, drafts and points only exist in Redis
  and come back empty. The response has `"rebuilt": true`.

## Email

Set `MAIL_HOST`, `MAIL_PORT`, `MAIL_USERNAME`, `MAIL_PASSWORD` and
`MAIL_FROM_EMAIL` to send through SMTP; on port 587 STARTTLS is
negotiated automatically.

Leave `MAIL_HOST` empty and verification links are written to the server log
instead of emailed, which is usually what you want locally.

`APP_BASE_URL` is the public origin used to build those links.
