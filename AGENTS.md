# AGENTS.md

You are working on a Go backend project.
Follow the architecture and conventions below **strictly**. Do not invent new patterns.

---

## 1. Project Goal

Build and maintain a clean, consistent, production-ready Go backend.
Code must be maintainable, testable, and follow a clear layered architecture. Prefer small, focused changes that preserve existing patterns over large rewrites.

---

## 2. Canonical Structure

```text
backend/
├── cmd/
│   └── server/
│       └── main.go              # thin composition root only
├── internal/
│   ├── api/
│   │   ├── dto/                 # request/response structs + mapping
│   │   ├── handler/             # thin HTTP handlers
│   │   ├── middleware/          # HTTP middleware (auth, logging, etc.)
│   │   ├── response/            # shared JSON, decode, error helpers
│   │   └── server.go            # route registration / server setup
│   ├── config/                  # env-based configuration
│   ├── db/                      # SQLC generated code + SQL queries + migrations
│   │   ├── queries/             # *.sql source for SQLC
│   │   ├── migrations/          # schema migrations
│   │   └── *.go                 # generated only — do not edit by hand
│   ├── domain/                  # pure entities + domain errors
│   ├── mail/                    # email sending (or other outbound adapters)
│   ├── repository/
│   │   ├── *.go                 # interfaces
│   │   └── postgres/            # implementations (pgx)
│   ├── service/                 # business logic
│   └── util/                    # pure helpers (JWT, password, etc.)
├── sqlc.yaml
├── go.mod
└── ...
```

Adjust package names only when a new bounded context genuinely needs its own package. Do not create parallel structures.

---

## 3. Architecture Rules (non-negotiable)

**Horizontal layered architecture**

Dependency direction must be:
```
api → service → repository → db
```

- `domain` has zero external dependencies (no net/http, no pgx, no third-party packages).
- SQLC-generated code lives only in `internal/db`. Never import it from service, api, or domain.
- Repository is the Anti-Corruption Layer:
  - Maps `db.*` → `domain.*`
  - Translates infrastructure errors → domain errors

**Technology choices:**

- Use `pgx/v5` + `pgxpool`.
- Use the standard library `net/http` (Go 1.22+ ServeMux style).
- No Chi, Gin, Echo, or other routers/frameworks.
- Config is loaded from environment variables only.
- `cmd/server/main.go` must stay a thin composition root: load config, create pool, wire repositories → services → handlers/server, start with graceful shutdown. Nothing else.

---

## 4. Error Handling Strategy

Follow this exact flow:

```
Database / infrastructure error (pgx.ErrNoRows, unique violation, …)
        ↓
Repository translates → domain error
        ↓
Service applies business rules (may wrap or replace the error)
        ↓
Handler maps domain error → HTTP status + JSON
```

### Domain errors

Define and keep sentinel errors in `domain/errors.go`, for example:

```go
var (
    ErrNotFound              = errors.New("not found")
    ErrInvalidCredentials    = errors.New("invalid credentials")
    ErrEmailNotVerified      = errors.New("email not verified")
    ErrEmailAlreadyExists    = errors.New("email already exists")
    ErrUsernameAlreadyExists = errors.New("username already exists")
    // … feature-specific domain errors
)
```

Add new domain errors only when they represent a business rule, not an infrastructure detail.

### Handler error mapping

All HTTP error mapping must go through `internal/api/response/errors.go` (or the shared response package).

Handlers must not contain large switch statements that map errors to status codes. Keep handlers thin.

Validation failures should return 400 with field-level details (via a shared validation error type).

---

## 5. Layer Responsibilities

| Layer | Responsibility | May import | Must not import |
|-------|----------------|------------|-----------------|
| `domain/` | Pure entities and domain errors | stdlib only | any other internal package, DB, HTTP |
| `repository/` | Interfaces + postgres implementations | domain, db, pgx | service, api |
| `service/` | All business logic | repository interfaces, domain, mail, util | db, api, net/http |
| `api/handler/` | Decode → call service → write response | dto, service, response, middleware | business logic, db, repository implementations |
| `api/dto/` | Request/response structs + domain→DTO mapping | domains | services, repositories |
| `api/middleware/` | Cross-cutting HTTP concerns (auth, etc.) | util, response, context helpers | services, repositories |
| `api/response/` | JSON helpers, decode, unified error writing | — | business logic |
| `util/` | Pure helpers (hash, JWT, …) | stdlib + minimal deps | domain entities with side effects |
| `cmd/server` | Wiring only | everything | business logic |

---

## 6. Concrete Conventions

### Repository

- Interfaces live in `repository/*.go`.
- Implementations live in `repository/postgres/`.
- Translate `pgx.ErrNoRows` → `domain.ErrNotFound`.
- Translate unique-constraint violations into the appropriate domain "already exists" error.
- Never leak SQLC models or pgx types outside the repository package.

### Service

- Contains validation, orchestration, password checks, token generation, etc.
- Depends only on repository interfaces, outbound ports (`mail.Sender`, …), and util.
- Returns domain errors or a structured validation error.
- Does not know about HTTP status codes.

### Handlers

- Extremely thin.
- Decode request body / query / path → call one service method → write success or let the shared error helper write the failure.
- No business rules, no direct DB access.

### Auth / protected routes

- JWT extraction and validation belong in middleware.
- Middleware places the authenticated user ID (or claims) into `context.Context`.
- Protected routes are registered with the auth middleware; handlers read the user ID from context.

### Configuration

- All runtime config comes from environment variables.
- `config` package exposes a typed struct loaded once at startup.
- Fail fast on missing required values.

### Database

- Schema changes go through migrations.
- Queries are written as SQL files under `internal/db/queries/` and generated by SQLC.
- Never hand-edit generated files under `internal/db/`.

---

## 7. What you must do

- Audit code against the rules above and fix inconsistencies.
- Keep error handling uniform (repository → domain → service → response helper).
- Wire everything in `main.go` / the server constructor; avoid hidden global state.
- Ensure protected routes use the auth middleware.
- Prefer small, focused packages and clear names.
- Delete dead or unused code.
- Keep the public surface of each package minimal.

---

## 8. What you must NOT do

- Do not add external HTTP routers or web frameworks.
- Do not put business logic in handlers or middleware.
- Do not import `internal/db` from service or api.
- Do not return SQLC models from service or handler layers.
- Do not keep dead/unused code.
- Do not change the overall folder structure unless absolutely necessary for a new bounded context.
- Do not introduce global mutable state or init-time side effects beyond configuration loading.
- Do not bypass the repository layer to talk to the database from services or handlers.

---

## 9. Deliverable checklist

After any change the project should:

- [ ] Compile cleanly (`go build ./...`)
- [ ] Respect the dependency direction (api → service → repository → db)
- [ ] Use domain errors end-to-end; no raw pgx or SQL errors escape the repository
- [ ] Have thin handlers and a thin `main.go`
- [ ] Load config only from the environment
- [ ] Keep generated SQLC code untouched and confined to `internal/db`
- [ ] Remain free of unused code and inconsistent patterns

When in doubt, prefer the existing pattern in the codebase over inventing a new one.
