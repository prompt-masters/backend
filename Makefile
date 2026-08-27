up:
	docker compose up -d

down:
	docker compose down

fmt:
	go fmt ./...

run:
	go build -o myapp ./cmd/server && ./myapp

migrate-up:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" down

migrate-create:
	goose -dir internal/db/migrations create $1 sql
