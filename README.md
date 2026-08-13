# Prompt Masters Backend API

Backend API for Prompt Masters, built with Go.

## Requirements

- Go 1.26.5+
- Docker & Docker Compose

## Getting Started

Start the database:

```sh
make up
```

Run the server:

```sh
go mod tidy
go run ./cmd/server
```
