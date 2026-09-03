-include .env

export

.PHONY: up logs down fmt run test migrate-up migrate-down migrate-create sqlc backend-fmt-check backend-lint check

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

backend-fmt-check:
	@UNFORMATTED="$$(find . -type f -name '*.go' -exec gofmt -l {} +)"; \
	if [ -n "$$UNFORMATTED" ]; then \
		echo "Go files are not formatted:"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi
	@echo "Backend formatting OK"

backend-lint:
	go vet ./...
	@echo "Backend vet OK"

check: backend-fmt-check backend-lint test
	@echo "All backend checks passed."

migrate-up:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" down

migrate-create:
	goose -dir internal/db/migrations create $1 sql

sqlc:
	sqlc generate
