-include .env
export

up:
	docker compose up -d --build

logs:
	docker compose logs -f app

down:
	docker compose down

run:
	go build -o myapp ./cmd/server && ./myapp

test:
	go test ./...

migrate-up:
	migrate -path ./migrations -database "$(DATABASE_URL)" up

migrate-down:
	migrate -path ./migrations -database "$(DATABASE_URL)" down 1
