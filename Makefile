-include .env
export

up:
	docker compose up -d --build

logs:
	docker compose logs -f app

down:
	docker compose down

fmt:
	go fmt ./...

run:
	go build -o myapp ./cmd/server && ./myapp

test:
	go test ./...

migrate-up:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" down

migrate-create:
	goose -dir internal/db/migrations create "$(NAME)" sql

sqlc:
	sqlc generate
