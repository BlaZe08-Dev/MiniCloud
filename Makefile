.PHONY: all build test test-race fmt vet run-coordinator run-worker demo clean db-up db-down db-psql db-reset-test test-db

BIN_DIR := bin

all: fmt vet build test-race

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/coordinator ./cmd/coordinator
	go build -o $(BIN_DIR)/worker ./cmd/worker
	go build -o $(BIN_DIR)/mc ./cmd/mc
	go build -o $(BIN_DIR)/migrate ./cmd/migrate
	@echo "Build complete. Executables located in $(BIN_DIR)/"

test:
	go test -v ./...

test-race:
	go test -v -race ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

run-coordinator: build
	./$(BIN_DIR)/coordinator

run-worker: build
	./$(BIN_DIR)/worker

demo: build
	@echo "=== Starting MiniCloud Local Core Demo ==="
	@bash scripts/demo.sh

# Database provisioning targets (Slice 3b)
db-up:
	@docker compose -f deployments/docker-compose.yml --env-file .env up -d --wait
	@echo "PostgreSQL container is running and healthy on 127.0.0.1:5433"

db-down:
	@docker compose -f deployments/docker-compose.yml down
	@echo "PostgreSQL container stopped (named volumes preserved)"

db-psql:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -it postgres sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

db-reset-test:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -T postgres sh -c '\
		TARGET="minicloud_test"; \
		case "$$TARGET" in *_test) ;; *) echo "Refusing to reset non-test DB $$TARGET" >&2; exit 1 ;; esac; \
		psql -U "$$POSTGRES_USER" -d postgres -c "DROP DATABASE IF EXISTS minicloud_test;" && \
		psql -U "$$POSTGRES_USER" -d postgres -c "CREATE DATABASE minicloud_test;" && \
		psql -U "$$POSTGRES_USER" -d postgres -c "GRANT ALL PRIVILEGES ON DATABASE minicloud_test TO \"$$POSTGRES_USER\";"'
	@echo "Test database minicloud_test reset successfully."

test-db:
	@if [ ! -f .env ]; then echo ".env file missing" >&2; exit 1; fi
	@set -e; \
	URL=$$(grep -E '^MINICLOUD_TEST_DATABASE_URL=' .env | cut -d= -f2-); \
	if [ -z "$$URL" ]; then echo "MINICLOUD_TEST_DATABASE_URL not set in .env" >&2; exit 1; fi; \
	MINICLOUD_TEST_DATABASE_URL="$$URL" go test -v -race -count=1 ./internal/storage/postgres/...

clean:
	rm -rf $(BIN_DIR)
