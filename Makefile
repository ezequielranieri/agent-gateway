.PHONY: run test lint build migrate-up migrate-down sqlc-generate bootstrap docker-up docker-down

run:
	go run ./cmd/gateway

test:
	go test -race -count=1 ./...

lint:
	golangci-lint run ./...

build:
	go build -o bin/gateway ./cmd/gateway

sqlc-generate:
	sqlc generate

migrate-up:
	goose -dir migrations postgres "$(DATABASE_URL)" up

migrate-down:
	goose -dir migrations postgres "$(DATABASE_URL)" down

bootstrap:
	go run ./cmd/bootstrap

docker-up:
	docker compose up -d

docker-down:
	docker compose down -v

generate: sqlc-generate

ci: lint test build