# PostgreSQL Local Development, Testing, and Provisioning Specification

## 1. Overview & Host Environment

In MiniCloud V1 Phase 3, PostgreSQL provides durable storage for all system state. To support local development and continuous testing without polluting the host operating system:
- **Provisioning Model:** Containerized PostgreSQL using Docker Compose.
- **Host Context:** 
  - `psql` client is **not** installed on the host.
  - The system `postgresql` systemd service is disabled/not running.
  - Docker 29.8.0 (Snap package) and Docker Compose v5.5.1 are installed and operational without `sudo`.
- **Purpose Disclaimer:** The Docker Compose environment specified here is strictly for **local development and automated testing**. It is not an HA production deployment.

---

## 2. Docker Compose Design (`deployments/docker-compose.yml`)

> **Note:** This section documents the design specification. The actual file will be created in Phase 3 Slice 3b/3c upon owner approval.

### 2.1 Confinement & Storage Constraints
- **Snap Docker Confinement:** The snap installation of Docker can strictly access files located within `/home/blazeee/`. Therefore, the Compose file, environment files, and all project directories must remain inside the repository workspace (`/home/blazeee/MiniCloud`).
- **Named Volumes Preferred:** Development data persistence must use a Docker named volume (`minicloud_pgdata`) rather than host directory bind mounts to avoid snap permission and filesystem path confinement hazards.
- **Interface & Port Binding:**
  - Bound exclusively to `127.0.0.1` (never `0.0.0.0` or public interfaces).
  - Mapped to host port **5433** (`127.0.0.1:5433:5432`) to prevent collisions with any default host PostgreSQL instances running on port 5432.

### 2.2 Specification Design

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: minicloud-postgres
    restart: unless-stopped
    ports:
      - "127.0.0.1:5433:5432"
    environment:
      POSTGRES_USER: ${POSTGRES_USER:-minicloud_dev}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD:-minicloud_dev_secret}
      POSTGRES_DB: ${POSTGRES_DB:-minicloud}
    volumes:
      - minicloud_pgdata:/var/lib/postgresql/data
      - ./init-test-db.sh:/docker-entrypoint-initdb.d/01-init-test-db.sh:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U ${POSTGRES_USER:-minicloud_dev} -d ${POSTGRES_DB:-minicloud}"]
      interval: 5s
      timeout: 5s
      retries: 5

volumes:
  minicloud_pgdata:
```

### 2.3 Multiple Databases Initialization (`init-test-db.sh`)
PostgreSQL's default entrypoint initializes only the database specified in `POSTGRES_DB` (`minicloud`). To provide isolated testing without cross-test data pollution:
- An entrypoint initialization script creates the second database: `minicloud_test`.
```bash
#!/bin/sh
set -e

psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    CREATE DATABASE minicloud_test;
    GRANT ALL PRIVILEGES ON DATABASE minicloud_test TO "$POSTGRES_USER";
EOSQL
```

---

## 3. Secret Management & `.env` Hygiene

1. **Gitignore Policy:**
   - Real credentials, `.env` files, and local overrides must **never** be committed to version control.
   - `.env` is listed in `.gitignore`.
2. **Template File (`.env.example`):**
   - A committed `.env.example` file provides placeholder values:
     ```bash
     # Local PostgreSQL Development Credentials (DEV ONLY)
     POSTGRES_USER=minicloud_dev
     POSTGRES_PASSWORD=minicloud_dev_secret
     POSTGRES_DB=minicloud
     
     # Connection URLs
     MINICLOUD_DATABASE_URL=postgres://minicloud_dev:minicloud_dev_secret@127.0.0.1:5433/minicloud?sslmode=disable
     MINICLOUD_TEST_DATABASE_URL=postgres://minicloud_dev:minicloud_dev_secret@127.0.0.1:5433/minicloud_test?sslmode=disable
     ```

---

## 4. Integration Test Execution & Safety Guardrails

Automated tests interacting with PostgreSQL must adhere to strict isolation and destructive-safety rules:

### Guardrail 1: Graceful Skip when Database is Unavailable
Tests requiring a live database must inspect `MINICLOUD_TEST_DATABASE_URL`. If empty:
```go
if os.Getenv("MINICLOUD_TEST_DATABASE_URL") == "" {
    t.Skip("skipping postgres integration test: MINICLOUD_TEST_DATABASE_URL not set")
}
```
This guarantees that standard unit tests (`go test ./...`) continue to pass cleanly in environments without Docker or PostgreSQL.

### Guardrail 2: Hard Database Name Suffix Enforcement (`_test`)
To permanently eliminate the risk of automated test suites truncating or dropping development or production data, test suites **must parse the target database name** before executing schema setup or cleanup:
```go
dbURL := os.Getenv("MINICLOUD_TEST_DATABASE_URL")
cfg, err := pgxpool.ParseConfig(dbURL)
if err != nil {
    t.Fatalf("invalid database URL: %v", err)
}

if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
    t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", cfg.ConnConfig.Database)
}
```

### Guardrail 3: Clean Isolation Between Tests
Each integration test either:
- Runs in an isolated transaction that rolls back (`defer tx.Rollback(ctx)`).
- Or truncates test tables between runs within the dedicated `minicloud_test` database.

---

## 5. Planned Makefile Targets (Design Specification)

> **Note:** These targets will be added to the project `Makefile` in Phase 3 Slice 3b/3c.

```makefile
# Database lifecycle targets
db-up:
	@echo "Starting local PostgreSQL container..."
	docker compose -f deployments/docker-compose.yml --env-file .env up -d

db-down:
	@echo "Stopping local PostgreSQL container..."
	docker compose -f deployments/docker-compose.yml down

# Run psql inside the container (eliminates need for host psql client)
db-psql:
	docker compose -f deployments/docker-compose.yml exec -it postgres psql -U minicloud_dev -d minicloud

db-psql-test:
	docker compose -f deployments/docker-compose.yml exec -it postgres psql -U minicloud_dev -d minicloud_test

# Run PostgreSQL-backed integration tests
test-db:
	@if [ -z "$$MINICLOUD_TEST_DATABASE_URL" ]; then \
		echo "Exporting default MINICLOUD_TEST_DATABASE_URL..."; \
		export MINICLOUD_TEST_DATABASE_URL="postgres://minicloud_dev:minicloud_dev_secret@127.0.0.1:5433/minicloud_test?sslmode=disable"; \
		go test -v -race -count=1 ./tests/integration/...; \
	else \
		go test -v -race -count=1 ./tests/integration/...; \
	fi
```
