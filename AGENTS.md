# AGENTS.md

You are working on an existing Go backend project.  
Follow the architecture and conventions below **strictly**. Do not invent new patterns.

---

## 1. Project Goal

Finish and clean up the **Authentication** feature so the codebase becomes consistent, maintainable, and production-ready.

Current state: the project is messy. Some files exist but are incomplete, inconsistent, or poorly wired. Your job is to make everything coherent.

---

## 2. Current Structure (source of truth)

```text
backend/
├── cmd/server/main.go
├── internal/
│   ├── api/
│   │   ├── dto/auth.go
│   │   ├── handler/auth.go
│   │   ├── middleware/auth.go
│   │   ├── response/          # shared HTTP helpers
│   │   │   ├── decode.go
│   │   │   ├── errors.go
│   │   │   └── response.go
│   │   └── router.go
│   ├── config/config.go
│   ├── db/                    # SQLC generated only
│   ├── domain/
│   │   ├── errors.go
│   │   ├── user.go
│   │   └── verification_token.go
│   ├── mail/
│   ├── repository/
│   │   ├── user.go
│   │   ├── verification_token.go
│   │   └── postgres/
│   │       ├── user.go
│   │       ├── verification_token.go
│   │       └── helpers.go
│   ├── service/auth.go
│   └── util/
│       ├── jwt.go
│       └── password.go
├── sqlc.yaml
└── ...

3. Architecture Rules (non-negotiable)

Horizontal layered architecture
Dependency direction must be:textapi → service → repository → db
domain has zero external dependencies
SQLC generated code lives only in internal/db
Repository is the Anti-Corruption Layer:
Maps db.* → domain.*
Translates infrastructure errors → domain errors

Use pgx/v5 + pgxpool
Use standard library net/http (Go 1.22+ style)
No Chi, Gin, Echo, or other routers
Config is loaded from environment variables only
main.go must stay a thin composition root


4. Error Handling Strategy
Follow this exact flow:
textDatabase error (pgx.ErrNoRows, unique violation…)
        ↓
Repository translates → domain error
        ↓
Service applies business rules (may change the error)
        ↓
Handler maps domain error → HTTP status + JSON
Domain errors (define/keep in domain/errors.go)
Govar (
    ErrNotFound                 = errors.New("not found")
    ErrInvalidCredentials       = errors.New("invalid credentials")
    ErrEmailNotVerified         = errors.New("email not verified")
    ErrEmailAlreadyExists       = errors.New("email already exists")
    ErrUsernameAlreadyExists    = errors.New("username already exists")
    ErrInvalidVerificationToken = errors.New("invalid verification token")
    ErrVerificationTokenExpired = errors.New("verification token expired")
)
Handler error mapping
All HTTP error mapping must go through internal/api/response/errors.go.

Handlers must not contain large switch statements for errors.

5. Required Auth Features
Implement / finish these endpoints:






























MethodPathDescriptionPOST/api/v1/auth/registerRegister new userPOST/api/v1/auth/loginLogin → returns JWTPOST/api/v1/auth/verify-emailVerify email with tokenGET/api/v1/auth/meGet current user (protected)
Business rules

Registration creates a user + verification token and sends email
Login requires verified email
Passwords are hashed with the existing util.HashPassword
JWT is generated with util.GenerateJWT
Invalid credentials must not reveal whether the email exists
Validation errors return 400 with field details


6. Layer Responsibilities
domain/

Pure entities and domain errors only
No validation logic that depends on HTTP or database

repository/

Interfaces in repository/*.go
Implementations in repository/postgres/
Only place allowed to import internal/db
Must translate pgx.ErrNoRows → domain.ErrNotFound
Must translate unique violations → ErrEmailAlreadyExists / ErrUsernameAlreadyExists

service/auth.go

Contains all business logic
Depends only on repository interfaces + mail.Sender + util
Performs validation, password checks, token generation, etc.
Returns domain errors or *ValidationError

api/handler/

Extremely thin
Decode request → call service → write response
No business logic

api/dto/

Request and response structs only
Mapping functions from domain → response DTOs

api/middleware/auth.go

Extracts and validates JWT
Puts userID into context

api/response/

Shared helpers: JSON, Decode, WriteError

cmd/server/main.go

Load config
Create pgxpool
Construct repositories → services → handlers/router
Start server with graceful shutdown
Nothing else


7. What you must do

Audit every existing file for consistency with the rules above
Fix or complete incomplete implementations
Make error handling uniform
Ensure main.go correctly wires all dependencies
Make sure protected routes use the auth middleware
Remove any code that violates the dependency direction
Keep the code clean and idiomatic


8. What you must NOT do

Do not add new external routers or frameworks
Do not put business logic in handlers
Do not import internal/db from service or api layers
Do not return SQLC models from service or handler
Do not keep dead/unused code
Do not change the overall folder structure unless absolutely necessary


9. Deliverable
After finishing, the project should:

Compile cleanly
Have a coherent auth flow (register → verify email → login → access protected route)
Follow the architecture rules without exceptions
Have clear, consistent error handling
Have a thin and readable main.go

Start by examining the current files, then make the necessary changes.
