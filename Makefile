.PHONY: run test lint build migrate-up migrate-down sqlc-generate bootstrap docker-up docker-down docker-staging-up docker-staging-down docker-staging-logs staging-deploy

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

docker-staging-up:
	docker compose -f docker-compose.staging.yml --env-file .env.staging up -d

docker-staging-down:
	docker compose -f docker-compose.staging.yml --env-file .env.staging down -v

docker-staging-logs:
	docker compose -f docker-compose.staging.yml --env-file .env.staging logs -f

staging-deploy:
	@echo "Building staging image..."
	docker build -t agent-gateway:v1.0.0-rc1 .
	@echo "Starting staging stack..."
	docker compose -f docker-compose.staging.yml --env-file .env.staging up -d
	@echo "Waiting for services to be healthy..."
	powershell -Command "Start-Sleep -Seconds 10"
	@echo "Running migrations..."
	docker compose -f docker-compose.staging.yml --env-file .env.staging exec gateway goose -dir migrations postgres "$$DATABASE_URL" up
	@echo "Staging deployed! Check http://localhost:8080/health"

generate: sqlc-generate

ci: lint test build