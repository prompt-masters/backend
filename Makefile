up:
	docker compose up -d

down:
	docker compose down

run:
	go build -o myapp ./cmd/server && ./myapp
