# MiniCloud

MiniCloud is a learning-oriented but engineering-serious distributed job-execution platform written in Go (standard library plus pgx v5), with PostgreSQL 16 as the durable source of truth. A central coordinator accepts computational jobs, independent worker processes pull and execute allowlisted tasks under leases, lifecycle transitions are validated by a central domain state machine, and state changes are recorded in durable storage. MiniCloud is designed to explore, implement, and measure real distributed-systems behavior—state transitions, worker coordination, leases, bounded retries, and failure recovery. It is not a production-ready cloud service, does not support multi-tenancy, does not expose APIs over untrusted public networks, and never runs arbitrary untrusted shell commands on the host.

## Status

- **Development Stage:** Pre-production (active V1 implementation).
- **Security Posture:** The HTTP API and worker endpoints currently lack authentication, authorization, and TLS (documented as accepted risk SEC-004 in [docs/security/findings.md](docs/security/findings.md)); network security is deferred to V3. The coordinator defaults to binding strictly on localhost (`127.0.0.1:8080`) and must not be exposed to untrusted networks.
- **Living Status:** The authoritative implementation progress, active milestones, and verified test passes are tracked in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md).

## What Works Today

The following capabilities are implemented and verified in the codebase (`cmd/`, `internal/`):

- **Coordinator Daemon (`cmd/coordinator`):** HTTP control plane managing job submissions, worker registrations, heartbeat tracking, and dispatching attempts. Supports in-memory and PostgreSQL storage backends.
- **Worker Daemon (`cmd/worker`):** Autonomous agent executing allowlisted tasks (`echo`, `sleep`, `sha256_loop`) with bounded concurrency slots and periodic heartbeats.
- **Command-Line Interface (`cmd/mc`):** CLI tool for job submission, job status inspection, attempt history retrieval, and worker capacity monitoring.
- **Database Migrations (`cmd/migrate`):** Forward-only embedded SQL migrator with PostgreSQL transaction advisory locking (`pg_advisory_xact_lock`) and schema checksum verification.
- **Domain State Machine (`internal/domain`):** Central entity lifecycle validation enforcing valid transitions across job states (`PENDING`, `QUEUED`, `RUNNING`, `SUCCEEDED`, `FAILED`, `CANCEL_REQUESTED`, `CANCELLED`) and attempt states (`PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `LOST`, `CANCELLED`).
- **Transactional Pull Dispatch (`fifo_pull`):** Single-transaction FIFO work acquisition in PostgreSQL using `SELECT ... FOR UPDATE SKIP LOCKED` on eligible jobs with worker slot capacity checks.
- **Health and Readiness Probes:** Liveness check at `/healthz` and database connectivity probe at `/readyz` mapping connection failures to HTTP 503 `SERVICE_UNAVAILABLE`.
- **API Boundary Protections:** Worker ID and route parameter validation (`^[a-zA-Z0-9_-]+$`, max 128 chars), numeric bounds validation on attempts (1..20) and timeouts (1..86400s), and payload size limits (64 KiB).
- **Outage Resilience:** Worker client exponential backoff with full jitter on HTTP 5xx responses, and automatic worker re-registration upon coordinator restart.

## Architecture

MiniCloud consists of four core executables and an authoritative PostgreSQL database:

- **Coordinator (`cmd/coordinator`):** Serves the HTTP API (`internal/api`), tracks worker registrations and heartbeats, validates transitions through the domain FSM (`internal/domain`), and orchestrates job dispatching.
- **Worker (`cmd/worker`):** Pulls assigned work from the coordinator, runs allowlisted tasks inside bounded execution slots (`internal/executor`), sends periodic heartbeats, and reports task results.
- **CLI (`cmd/mc`):** Client interface for submitting jobs and querying coordinator status over HTTP.
- **Migrator (`cmd/migrate`):** Applies forward-only SQL migrations to PostgreSQL before the coordinator starts.
- **PostgreSQL 16 (`deployments/docker-compose.yml`):** Durable source of truth for jobs, attempts, workers, execution results, and audit events.

```text
+-----------------------------------------------------------------+
|                          Client / CLI                           |
|                           (cmd/mc)                              |
+-----------------------------------------------------------------+
                                |
                                | HTTP REST (/v1/jobs, /v1/workers)
                                v
+-----------------------------------------------------------------+
|                      Coordinator Service                        |
|                       (cmd/coordinator)                         |
|                                                                 |
|   +---------------------+             +---------------------+   |
|   |   HTTP API Server   |             |  Domain State Mach. |   |
|   |   (internal/api)    |             |  (internal/domain)  |   |
|   +---------------------+             +---------------------+   |
|              |                                   |              |
|              +-----------------+-----------------+              |
|                                |                                |
|                                v                                |
|   +---------------------------------------------------------+   |
|   |                     Storage Driver                      |   |
|   |         (internal/storage/inmemory or postgres)         |   |
|   +---------------------------------------------------------+   |
+-----------------------------------------------------------------+
          ^                              |
          | HTTP Poll / Heartbeat        | PostgreSQL 16
          | (/v1/workers/*)              | (deployments/docker-compose.yml)
          v                              v
+-----------------------+      +----------------------------------+
|     Worker Agent      |      |           PostgreSQL             |
|     (cmd/worker)      |      |   jobs, attempts, workers,       |
|                       |      |   job_results, job_events        |
|  +-----------------+  |      +----------------------------------+
|  | Task Executor   |  |
|  | (echo, sleep,   |  |
|  |  sha256_loop)   |  |
|  +-----------------+  |
+-----------------------+
```

## Quick Start

### 1. In-Memory Mode (Primary Quick Start)

The in-memory storage driver requires no external database container or configuration.

Build all executables:

```bash
make build
```

Terminal 1: Start the coordinator in background:

```bash
MINICLOUD_STORAGE_DRIVER=memory MINICLOUD_HTTP_ADDR=127.0.0.1:8080 ./bin/coordinator
```

Terminal 2: Start a worker process:

```bash
MINICLOUD_COORDINATOR_URL=http://127.0.0.1:8080 MINICLOUD_WORKER_ID=worker-1 MINICLOUD_WORKER_SLOTS=2 ./bin/worker
```

Terminal 3: Interact with the system using `mc`:

Check registered workers:

```bash
./bin/mc workers
```

Output:
```text
Registered Workers (1 total):
WORKER ID   STATUS    AVAILABLE/TOTAL   LAST HEARTBEAT   TASKS
worker-1    HEALTHY   2/2               11:47:13         [echo sleep sha256_loop]
```

Submit an `echo` task:

```bash
./bin/mc submit --task echo --payload '{"message":"hello minicloud"}'
```

Output:
```text
Job Submitted Successfully!
  ID:           job_1_9fb43b921313
  Task:         echo
  State:        QUEUED
  Max Attempts: 3
```

Query the completed job:

```bash
./bin/mc get job_1_9fb43b921313
```

Output:
```text
Job Details:
  ID:           job_1_9fb43b921313
  Task:         echo
  State:        SUCCEEDED
  Attempt ID:   att_1_46ceaa3f05b8
  Created At:   2026-10-28T11:47:14Z
  Updated At:   2026-10-28T11:47:14Z
  Result:
    Exit Code:  0
    Output:     {"echo":{"message":"hello minicloud"}}
```

Inspect attempt execution history:

```bash
./bin/mc attempts job_1_9fb43b921313
```

Output:
```text
Attempts for Job job_1_9fb43b921313 (1 total):
NO   ATTEMPT ID           WORKER     STATE       EXIT   STARTED
1    att_1_46ceaa3f05b8   worker-1   COMPLETED   0      11:47:14
```

### 2. PostgreSQL Mode (Durable Persistence)

*(Note: The commands in this subsection describe the PostgreSQL provisioning path and are labeled not executed in this task)*

1. Copy `.env.example` to `.env` and set local credentials:

```bash
cp .env.example .env
```

Validate the Docker Compose configuration:

```bash
docker compose --env-file .env -f deployments/docker-compose.yml config
```

2. Start the PostgreSQL container:

```bash
make db-up
```

3. Run database migrations:

```bash
./bin/migrate
```

4. Start the coordinator with the PostgreSQL storage driver:

```bash
MINICLOUD_STORAGE_DRIVER=postgres MINICLOUD_DATABASE_URL="postgres://postgres:password@127.0.0.1:5433/minicloud?sslmode=disable" ./bin/coordinator
```

5. Start workers and submit jobs as shown in the in-memory quick start.

## Configuration

All configuration is loaded from environment variables:

| Environment Variable | Default Value | Used By | Source File | Description |
|---|---|---|---|---|
| `MINICLOUD_HTTP_ADDR` | `127.0.0.1:8080` | Coordinator | `internal/config/config.go` | HTTP server listening address and port |
| `MINICLOUD_STORAGE_DRIVER` | `memory` | Coordinator | `internal/config/config.go` | Storage engine (`memory` or `postgres`) |
| `MINICLOUD_DATABASE_URL` | `""` | Coordinator, Migrator | `internal/config/config.go`, `cmd/migrate/main.go` | PostgreSQL database connection URL |
| `MINICLOUD_HEARTBEAT_INTERVAL` | `2s` | Coordinator, Worker | `internal/config/config.go` | Frequency of worker heartbeat transmissions |
| `MINICLOUD_WORKER_TIMEOUT` | `10s` | Coordinator | `internal/config/config.go` | Liveness window before an uncommunicative worker is declared dead (>= 3x heartbeat interval) |
| `MINICLOUD_REAPER_INTERVAL` | `5s` (`workerTimeout / 2`) | Coordinator | `internal/config/config.go` | Interval for coordinator worker liveness sweep |
| `MINICLOUD_DEFAULT_MAX_ATTEMPTS` | `3` | Coordinator | `internal/config/config.go` | Default retry attempt limit for submitted jobs |
| `MINICLOUD_MAX_JOB_RUNTIME` | `60s` | Coordinator | `internal/config/config.go` | Global upper ceiling for job execution duration |
| `MINICLOUD_LOG_LEVEL` | `info` | Coordinator, Worker | `internal/config/config.go` | Structured log level (`debug`, `info`, `warn`, `error`) |
| `MINICLOUD_DB_MAX_CONNS` | `20` | Coordinator | `internal/config/config.go` | Maximum connections in the PostgreSQL pool |
| `MINICLOUD_DB_MIN_CONNS` | `2` | Coordinator | `internal/config/config.go` | Minimum idle connections in the PostgreSQL pool |
| `MINICLOUD_DB_QUERY_TIMEOUT` | `5s` | Coordinator | `internal/config/config.go` | Per-query context timeout for coordinator SQL operations |
| `MINICLOUD_COORDINATOR_URL` | `http://127.0.0.1:8080` | Worker, CLI (`mc`) | `internal/config/config.go`, `cmd/mc/main.go` | Base URL of the coordinator service |
| `MINICLOUD_WORKER_ID` | `""` (auto-generated) | Worker | `internal/config/config.go`, `cmd/worker/main.go` | Identifier for the worker (defaults to `worker_<hostname>_<pid>`) |
| `MINICLOUD_WORKER_SLOTS` | `4` | Worker | `internal/config/config.go` | Concurrency execution capacity of the worker |
| `MINICLOUD_TEST_DATABASE_URL` | `""` | Test Suites | `internal/storage/postgres/testhelper_test.go` | Isolated database URL for PostgreSQL integration tests (DB name must end in `_test`) |

Database container provisioning in `deployments/docker-compose.yml` also uses:
- `POSTGRES_USER` (default: `postgres`)
- `POSTGRES_PASSWORD` (required in `.env`)
- `POSTGRES_DB` (default: `minicloud`)
- `POSTGRES_PORT` (default: `5433`, mapped to container port `5432`)

## CLI Usage

Command options from `mc --help`:

```text
MiniCloud CLI (mc)

Usage:
  mc submit --task <type> --payload <json> [--timeout <sec>] [--max-attempts <num>]
  mc get <job_id>
  mc attempts <job_id>
  mc workers

Environment Variables:
  MINICLOUD_COORDINATOR_URL   Coordinator HTTP API base URL (default: http://127.0.0.1:8080)
```

Subcommands:
- `mc submit --task <type> --payload <json> [--timeout <sec>] [--max-attempts <num>]`: Submits a job to the coordinator. Allowlisted task types are `echo`, `sleep`, and `sha256_loop`.
- `mc get <job_id>`: Displays job status, current attempt number, timestamps, and execution output.
- `mc attempts <job_id>`: Lists all attempts recorded for a job, including assigned worker, attempt state, and exit code.
- `mc workers`: Displays active registered workers, health status, capacity slots, and last heartbeat timestamps.

## HTTP API

The HTTP API is implemented in `internal/api` and fully specified in [docs/protocol.md](docs/protocol.md):

### System Health
- `GET /healthz` - Basic coordinator service liveness check (returns HTTP 200 `{"status":"ok"}`).
- `GET /readyz` - Readiness probe verifying database reachability via `repo.Ping` (returns HTTP 200 or HTTP 503 `SERVICE_UNAVAILABLE`).

### Job Lifecycle
- `POST /v1/jobs` - Submit a new job payload. Returns HTTP 201 `CREATED` with job metadata.
- `GET /v1/jobs/{id}` - Fetch job details and execution result.
- `GET /v1/jobs/{id}/attempts` - List execution attempts for a job.
- `POST /v1/jobs/{id}/cancel` - Request cooperative cancellation of a job. Returns HTTP 200.

### Worker Protocol
- `POST /v1/workers/register` - Register a worker, its capacity slots, and supported task types.
- `POST /v1/workers/{id}/heartbeat` - Send periodic liveness ping and report available capacity slots.
- `POST /v1/workers/{id}/poll` - Poll for next available job assignment (`fifo_pull`).
- `POST /v1/jobs/{id}/attempts/{attempt_id}/result` - Report completion or failure of an assigned attempt.
- `GET /v1/workers` - Operator endpoint listing registered workers.

## Project Layout

```text
.
├── cmd/                        # Application entrypoints
│   ├── coordinator/            # Coordinator daemon main package
│   ├── migrate/                # PostgreSQL database migration runner
│   ├── mc/                     # Operator and developer command-line interface
│   └── worker/                 # Worker agent main package
├── deployments/                # Docker compose file and database initialization scripts
├── docs/                       # Architecture documentation, ADRs, state machine, and security specs
├── internal/                   # Private internal libraries
│   ├── api/                    # HTTP server, routing, endpoint handlers, and payload models
│   ├── config/                 # Environment variable loading, validation, and defaults
│   ├── coordinator/            # Coordinator core service logic and attempt tracking
│   ├── domain/                 # Core domain models (Job, Attempt, Worker) and central FSM
│   ├── executor/               # Task execution interface and allowlisted in-process tasks
│   ├── queue/                  # Dispatch queue interface and in-memory queue implementation
│   ├── scheduler/              # Deterministic round-robin scheduler
│   ├── storage/                # Storage interfaces, in-memory repository, and PostgreSQL driver
│   └── worker/                 # Worker client loop, heartbeat manager, and execution runner
├── scripts/                    # Helper scripts for demos and verification
└── tests/                      # Test suites
    ├── architecture/           # Boundary and package dependency guard tests
    ├── docs/                   # Documentation citation and consistency checker
    ├── integration/            # Multi-process end-to-end integration tests
    └── review/                 # Review probe tests
```

## Testing

### Make Targets

- `make build`: Compiles all binaries into `bin/` (`coordinator`, `worker`, `mc`, `migrate`).
- `make test`: Runs standard unit and integration tests (`go test -v ./...`).
- `make test-race`: Runs the test suite with the Go race detector enabled (`go test -v -race ./...`).
- `make test-docs`: Verifies documentation citations, links, and rules via `tests/docs`.
- `make test-db`: Runs PostgreSQL persistence tests against `MINICLOUD_TEST_DATABASE_URL` in `internal/storage/postgres`.
- `make fmt`: Automatically formats Go source code using `gofmt -s -w .`.
- `make vet`: Executes Go static code analysis (`go vet ./...`).
- `make demo`: Runs the automated multi-process local demo script (`scripts/demo.sh`).
- `make db-up`: Starts the PostgreSQL container via Docker Compose and waits for healthy status.
- `make db-down`: Stops the PostgreSQL container, preserving database volumes.
- `make db-psql`: Opens an interactive `psql` console in the database container.
- `make db-reset-test`: Resets the `minicloud_test` database (safely refuses non-test names).
- `make clean`: Removes compiled binaries from `bin/`.

### Test Guardrails and Warnings

- **Test Database Isolation:** All tests requiring PostgreSQL verify that the target database name ends strictly with `_test` (checked in `internal/storage/postgres/testhelper_test.go`). If `MINICLOUD_TEST_DATABASE_URL` is unset, database tests skip cleanly.
- **Container Interruption Warning:** Integration tests in `tests/integration/postgres_wiring_test.go` and `tests/integration/part_d_security_test.go` test coordinator recovery by stopping and restarting the PostgreSQL container (`docker compose stop postgres` / `start postgres`). Running these integration tests will temporarily stop the shared database container.

## Roadmap

MiniCloud development follows a three-version roadmap defined in [docs/roadmap.md](docs/roadmap.md):

- **V1 (Phases 0–4):** Core execution with allowlisted tasks, transactional FIFO pull dispatch (`fifo_pull`), PostgreSQL durability, lease management, bounded retries, cooperative cancellation, and post-Phase 4 assignment-based round-robin scheduling.
- **V2 (Phase 6 & Transport Evaluation):** Resource-aware scheduling policies, comparative benchmark generator, fault injection harness, and Redis Streams dispatch evaluation against the PostgreSQL baseline (ADR-0006). PostgreSQL remains the durable source of truth.
- **V3 (Docker Isolation & Multi-Machine):** Docker container task isolation behind the executor interface (ADR-0005), worker mutual TLS (mTLS), worker API token authentication, and multi-machine deployment hardening.

## Documentation Index

- [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md): Living handoff, completed milestones, and current phase progress.
- [docs/roadmap.md](docs/roadmap.md): Three-version release roadmap and exit criteria.
- [docs/architecture.md](docs/architecture.md): System components, data flows, and design invariants.
- [docs/state-machine.md](docs/state-machine.md): Central state machine, attempt states, and transition matrix.
- [docs/protocol.md](docs/protocol.md): HTTP API endpoints, request/response models, and error codes.
- [docs/failure-model.md](docs/failure-model.md): Failure classifications and recovery behaviors.
- [docs/schema.md](docs/schema.md): Database schema, constraints, and lock order.
- [docs/phase4-plan.md](docs/phase4-plan.md): Phase 4 implementation plan for leases, retries, and cancellation.
- [docs/fault-injection-plan.md](docs/fault-injection-plan.md): End-to-end failure injection test specifications.
- [docs/testing-postgres.md](docs/testing-postgres.md): PostgreSQL container setup and database testing guide.
- [docs/adr/](docs/adr/): Architecture Decision Records (ADR-0001 through ADR-0016).
- [docs/security/findings.md](docs/security/findings.md): Security findings register and status.
- [docs/security/threat-model.md](docs/security/threat-model.md): Threat model, trust boundaries, and attack vectors.
- [docs/security/field-limits.md](docs/security/field-limits.md): API field limits and database constraint reference.
- [docs/security/test-plan.md](docs/security/test-plan.md): Security and adversarial test suite definitions.
- [docs/security/security-testing.md](docs/security/security-testing.md): Security testing rules and verification methodology.

## Security

Security documentation and risk tracking are maintained in [docs/security/findings.md](docs/security/findings.md) and [docs/security/threat-model.md](docs/security/threat-model.md). In V1, coordinator and worker HTTP endpoints are unauthenticated (accepted risk SEC-004), mitigated by default binding to localhost (`127.0.0.1`). Field sizes, task parameters, and timeouts are bounded as specified in [docs/security/field-limits.md](docs/security/field-limits.md).

## Development Workflow

MiniCloud uses an agent-pair engineering workflow governed by [AGENTS.md](AGENTS.md). All code changes follow strict quality gates: zero `gofmt` violations, clean `go vet`, clean `go build`, clean `make test-docs`, race-detector test execution, and isolated per-worktree test databases.

## License

No license has been chosen yet.
