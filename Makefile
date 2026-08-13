up:
	docker compose up -d

down:
	docker compose down

run:
	go build ./cmd/server && ./server
