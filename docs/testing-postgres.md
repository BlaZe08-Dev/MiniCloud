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

### 2.2 Specification Design (`deployments/docker-compose.yml`)

```yaml
services:
  postgres:
    image: postgres:16-alpine
    container_name: minicloud-postgres
    restart: unless-stopped
    environment:
      POSTGRES_USER: ${POSTGRES_USER:-postgres}
      POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}
      POSTGRES_DB: ${POSTGRES_DB:-minicloud}
    ports:
      - "127.0.0.1:${POSTGRES_PORT:-5433}:5432"
    volumes:
      - minicloud_pgdata:/var/lib/postgresql/data
      - ./init-test-db.sh:/docker-entrypoint-initdb.d/init-test-db.sh:ro
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $$POSTGRES_USER -d $$POSTGRES_DB"]
      interval: 3s
      timeout: 3s
      retries: 10
      start_period: 3s

volumes:
  minicloud_pgdata:
    name: minicloud_pgdata
```

> **Recommendation / Hardening Suggestion:**  
> Currently `deployments/docker-compose.yml` specifies `POSTGRES_PASSWORD: ${POSTGRES_PASSWORD}` without a fallback default (requiring `.env` to be loaded via `--env-file .env`). A recommended future hardening improvement for the compose file is to use `${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}` so docker compose fails immediately with an explicit error message if the environment variable is unset or empty. *(Note: `deployments/docker-compose.yml` is kept untouched in Slice 3c per review instructions).*

### 2.3 Multiple Databases Initialization (`init-test-db.sh`)
PostgreSQL's default entrypoint initializes only the database specified in `POSTGRES_DB` (`minicloud`). To provide isolated testing without cross-test data pollution:
- An entrypoint initialization script (`deployments/init-test-db.sh`) provisions the secondary test database: `minicloud_test`.
- **Fresh Volume Rule:** The official PostgreSQL entrypoint only executes scripts in `/docker-entrypoint-initdb.d/` when the data volume is completely fresh/empty. If the container or named volume already exists, init scripts do not re-run.

---

## 3. Secret Management & `.env` Hygiene

1. **Gitignore Policy:**
   - Real credentials, `.env` files, and local overrides must **never** be committed to version control.
   - `.env` is listed in `.gitignore`.
2. **Template File (`.env.example`):**
   - A committed `.env.example` file provides placeholder values.
3. **Execution Hygiene:**
   - Never run `docker compose config`, `env`, `cat .env` or `set -x` in automated scripts or shell sessions.
   - Makefile recipes use `@`-prefixes to avoid leaking database URLs and passwords in terminal output.

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
if !strings.HasSuffix(dbName, "_test") {
    t.Fatalf("SAFETY VIOLATION: refusing to run test against database %q; name must end in '_test'", dbName)
}
```

### Guardrail 3: Schema-Per-Test Isolation
Each test creates and uses a dedicated unique schema (e.g. `test_<random_hex>`) via `SET search_path TO ...`, which is dropped during `t.Cleanup()`. This permits parallel test execution without table collision. Advisory locks are not per schema, though: the assignment pass key is database-wide by design (ADR-0017 INV-22, implementation note 15), so two test processes on one database make each other's passes skip, and the assignment tests fail. Run one test process at a time per test database.

---

## 5. Implemented Makefile Targets

```makefile
# Start local PostgreSQL container (waits for healthcheck)
db-up:
	@docker compose -f deployments/docker-compose.yml --env-file .env up -d --wait

# Stop local PostgreSQL container (named volume minicloud_pgdata preserved)
db-down:
	@docker compose -f deployments/docker-compose.yml down

# Run psql inside the container connected to the development database
db-psql:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -it postgres sh -c 'psql -U "$$POSTGRES_USER" -d "$$POSTGRES_DB"'

# Reset the test database only (verifies _test suffix, drops & recreates minicloud_test)
db-reset-test:
	@docker compose -f deployments/docker-compose.yml --env-file .env exec -T postgres sh -c '...'

# Run database tests with MINICLOUD_TEST_DATABASE_URL loaded from .env without echoing
test-db:
	@...

# Run security test suite against a dedicated per-agent test database (refuses minicloud_test early; override with ALLOW_SHARED_DB=1)
test-security:
	@...
```
