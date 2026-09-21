.PHONY: run test test-integration lint build migrate-up migrate-down sqlc-generate bootstrap docker-up docker-down docker-staging-up docker-staging-down docker-staging-logs staging-deploy check-disabled check-skip

run:
	go run ./cmd/gateway

test:
	go test -race -count=1 ./internal/...

test-integration:
	go test -tags integration -count=1 -v ./...

check-disabled:
	@if find . -name "*.disabled" -type f | grep -q .; then \
		echo "ERROR: Found disabled test files:"; \
		find . -name "*.disabled" -type f; \
		echo "All tests must be enabled. Remove .disabled suffix or delete the file."; \
		exit 1; \
	fi

check-skip:
	@output=$$(go test -tags integration -count=1 -v ./... 2>&1 || true); \
	echo "$$output"; \
	allowed_skips=("TEST_DATABASE_URL not set, skipping integration test"); \
	if [ "$$CI" = "true" ]; then \
		allowed_skips=("TEST_DATABASE_URL not set, skipping integration test"); \
		echo "=== CI MODE: Only TEST_DATABASE_URL skips allowed ==="; \
	else \
		allowed_skips=("Skipping integration test in short mode" "TEST_DATABASE_URL not set, skipping integration test" "Skipping WasmExecutor tests - need proper wasm test fixtures" "Skipping timeout test - httptest.Server blocks on close with hanging connections"); \
		echo "=== LOCAL MODE: Allowing short mode, WasmExecutor, timeout skips ==="; \
	fi; \
	skip_found=false; \
	prev_line=""; \
	while IFS= read -r line; do \
		if echo "$$line" | grep -q -- "--- SKIP"; then \
			allowed=false; \
			for pattern in "$${allowed_skips[@]}"; do \
				if echo "$$line" | grep -q -- "$$pattern" || echo "$$prev_line" | grep -q -- "$$pattern"; then \
					allowed=true; \
					break; \
				fi; \
			done; \
			if [ "$$allowed" = false ]; then \
				echo "ERROR: Unexpected SKIP found: $$line"; \
				skip_found=true; \
			fi; \
		fi; \
		prev_line="$$line"; \
	done <<< "$$output"; \
	if [ "$$skip_found" = true ]; then \
		exit 1; \
	fi

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

ci: lint check-disabled test test-integration check-skip build