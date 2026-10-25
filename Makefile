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

# The checks CI runs, in its order.
lint:
	@test -z "$$(gofmt -l .)" || { echo "not gofmt-formatted:"; gofmt -l .; exit 1; }
	go vet ./...
	sqlc diff

# Unit tests only: database and Redis tests skip without their URLs.
test-unit:
	env -u TEST_DATABASE_URL -u TEST_REDIS_ADDR go test -race -count=1 ./...

# Every test, against TEST_DATABASE_URL and TEST_REDIS_ADDR; none may skip.
test-integration:
	./scripts/integration-test.sh

migrate-up:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir internal/db/migrations postgres "$(DATABASE_URL)" down

seed:
	go run ./cmd/seed

migrate-create:
	goose -dir internal/db/migrations create "$(NAME)" sql

sqlc:
	sqlc generate
