# Dev Guide

## Project Structure

```
cmd/server/main.go           → entry point, wires everything together
internal/
  api/                       → HTTP handlers, response helpers
  config/config.go           → env loading
  db/
    migrations/              → goose SQL migrations
    queries/                 → sqlc SQL queries
    (generated)              → sqlc output (models.go, db.go, *.sql.go)
  domain/                    → domain models (plain structs, no DB deps)
  repository/                → repository interfaces
    postgres/                → postgres implementations
  service/                   → business logic
  util/                      → shared helpers (password hashing, etc.)
```

## Feature Flow (top-down)

When building a new feature, work in this order:

### 1. Domain (`internal/domain/`)

Define your model. Plain struct, no DB or HTTP imports.

```go
type Post struct {
    ID        uuid.UUID `json:"id"`
    Title     string    `json:"title"`
    Body      string    `json:"body"`
    AuthorID  uuid.UUID `json:"author_id"`
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

### 2. Migration (`internal/db/migrations/`)

Create via make:

```bash
make migrate-create create_posts
```

Write the SQL:

```sql
-- +goose Up
CREATE TABLE posts (
    id         UUID PRIMARY KEY,
    title      VARCHAR(255) NOT NULL,
    body       TEXT NOT NULL,
    author_id  UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS posts;
```

Run it:

```bash
make migrate-up
```

### 3. SQLC Queries (`internal/db/queries/`)

Create a query file:

```sql
-- internal/db/queries/posts.sql

-- name: CreatePost :one
INSERT INTO posts (id, title, body, author_id)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetPostByID :one
SELECT * FROM posts WHERE id = $1;

-- name: ListPostsByAuthor :many
SELECT * FROM posts WHERE author_id = $1 ORDER BY created_at DESC;
```

Generate:

```bash
sqlc generate
```

### 4. Repository Interface (`internal/repository/`)

Define the contract. Depends only on `domain` and `context`.

```go
type PostRepository interface {
    Create(ctx context.Context, title, body string, authorID uuid.UUID) (*domain.Post, error)
    GetByID(ctx context.Context, id uuid.UUID) (*domain.Post, error)
    ListByAuthor(ctx context.Context, authorID uuid.UUID) ([]domain.Post, error)
}
```

### 5. Repository Implementation (`internal/repository/postgres/`)

Implements the interface. Handles:
- UUID conversion (`pgUUID`, `uuidFromPg`)
- Mapping `db.*` models to `domain.*` models
- Password hashing (if applicable)

```go
type PostRepository struct {
    queries *db.Queries
}

func (r *PostRepository) Create(ctx context.Context, title, body string, authorID uuid.UUID) (*domain.Post, error) {
    created, err := r.queries.CreatePost(ctx, db.CreatePostParams{
        ID:       pgUUID(uuid.New()),
        Title:    title,
        Body:     body,
        AuthorID: pgUUID(authorID),
    })
    if err != nil {
        return nil, err
    }
    return toDomainPost(created), nil
}
```

### 6. Service (`internal/service/`)

Business logic. Depends on repository interfaces, not implementations.

```go
type PostService struct {
    posts repository.PostRepository
}

func (s *PostService) Create(ctx context.Context, title, body string, authorID uuid.UUID) (*domain.Post, error) {
    // validation, business rules, etc.
    return s.posts.Create(ctx, title, body, authorID)
}
```

### 7. API Handler (`internal/api/`)

HTTP layer. Parses request, calls service, writes response.

```go
func CreatePost(svc *service.PostService) api.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) error {
        var input struct {
            Title string `json:"title"`
            Body  string `json:"body"`
        }
        if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
            return api.WriteError(w, http.StatusBadRequest, "invalid request body")
        }

        post, err := svc.Create(r.Context(), input.Title, input.Body, userID)
        if err != nil {
            return api.WriteError(w, http.StatusInternalServerError, "failed to create post")
        }

        return api.WriteSuccess(w, post)
    }
}
```

### 8. Wire in `cmd/server/main.go`

```go
queries := db.New(pool)

postRepo := postgres.NewPostRepository(queries)
postService := service.NewPostService(postRepo)

mux.HandleFunc("POST /posts", api.WrapHandler(CreatePost(postService)))
```

## Order Summary

```
domain → migration → sqlc queries → sqlc generate → repository interface → repository impl → service → api handler → wire in main.go
```

## Commands

```bash
make up                    # start postgres
make migrate-up            # run migrations
make migrate-down          # rollback migrations
make migrate-create NAME   # create new migration file
make run                   # build and run server
sqlc generate              # regenerate sqlc code
```

## Rules

- Domain models never import `db`, `repository`, or `api` packages.
- Services depend on repository interfaces, never postgres implementations.
- API handlers never touch the database directly.
- Passwords are hashed in the repository layer, never in services or handlers.
- UUIDs are generated in Go (`uuid.New()`), not in the database.
