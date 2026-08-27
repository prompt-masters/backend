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
│       └── password.go                    # Bcrypt hash + verify
│
├── sqlc.yaml
├── Makefile
├── docker-compose.yaml
├── DEV_GUIDE.md                           # Feature development workflow
└── .env.example
```

## Tech Stack

- **Go** 1.26.5+
- **PostgreSQL** 16 (via Docker)
- **[goose](https://github.com/pressly/goose)** — database migrations
- **[sqlc](https://sqlc.dev)** — type-safe SQL query generation
- **[pgx/v5](https://github.com/jackc/pgx)** — PostgreSQL driver
- **bcrypt** — password hashing
- **google/uuid** — UUID generation

## Getting Started

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
| `make up` | Start postgres container |
| `make down` | Stop postgres container |
| `make run` | Build and run server |
| `make migrate-up` | Apply all migrations |
| `make migrate-down` | Rollback last migration |
| `make migrate-create <name>` | Create a new migration file |

## Development

See [DEV_GUIDE.md](DEV_GUIDE.md) for the full feature development workflow.

**Quick flow:** domain → migration → sqlc queries → `sqlc generate` → repository → service → api handler → wire in main.go
