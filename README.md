# MiniCloud

MiniCloud is a learning-oriented distributed job-execution platform written in Go (standard library plus pgx v5), with PostgreSQL 16 as the durable source of truth. A central coordinator accepts computational jobs, independent worker processes pull and execute allowlisted tasks under a per-attempt lease (design: [ADR-0014](docs/adr/0014-lease-model-and-expiry.md) and [docs/protocol.md](docs/protocol.md)), lifecycle transitions are validated by a central domain state machine, and state changes are recorded in durable storage. MiniCloud is designed to explore, implement, and measure real distributed-systems behavior—state transitions, worker coordination, leases, bounded retries, and failure recovery. It is not a production-ready cloud service, does not support multi-tenancy, is not designed to be exposed to untrusted networks, and never runs arbitrary untrusted shell commands on the host.

## Status

- **Development Stage:** Pre-production (active V1 implementation).
- **Phase:** V1, Phase 4 in progress. The current slice and verified test passes are tracked in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md).
- **Security Posture:** The HTTP API and worker endpoints currently lack authentication, authorization, and TLS (documented as accepted risk SEC-004 in [docs/security/findings.md](docs/security/findings.md)); network security is deferred to V3. The coordinator defaults to binding strictly on localhost (`127.0.0.1:8080`) and must not be exposed to untrusted networks.
- **Living Status:** The authoritative implementation progress, active milestones, and verified test passes are tracked in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md).

## What Works Today

The following capabilities are implemented and verified in the codebase (`cmd/`, `internal/`):

- **Coordinator Daemon (`cmd/coordinator`):** HTTP control plane managing job submissions, worker registrations, heartbeat tracking, and dispatching attempts. Supports in-memory and PostgreSQL storage backends.
- **Worker Daemon (`cmd/worker`):** Autonomous agent executing allowlisted tasks (`echo`, `sleep`, `sha256_loop`) with bounded concurrency slots and periodic heartbeats.
- **Command-Line Interface (`cmd/mc`):** CLI tool for job submission, job status inspection, attempt history retrieval, and worker capacity monitoring.
- **Database Migrations (`cmd/migrate`):** Forward-only embedded SQL migrator with PostgreSQL transaction advisory locking (`pg_advisory_xact_lock`) and schema checksum verification.
- **Domain State Machine (`internal/domain`):** Central entity lifecycle validation enforcing valid transitions across job states (`SUBMITTED`, `QUEUED`, `RUNNING`, `CANCEL_REQUESTED`, `RETRYING`, `SUCCEEDED`, `FAILED`, `CANCELLED`, `REJECTED`) and attempt states (`PENDING`, `RUNNING`, `COMPLETED`, `FAILED`, `TIMED_OUT` (reserved, not written in V1), `CANCELLED`, `LOST`); the full transition matrix is specified in [docs/state-machine.md](docs/state-machine.md) (Section 3 and Section 4).
- **Dispatch Policies:** The default dispatch policy is assignment-based round-robin (`round_robin` since Slice P4-17), with single-transaction FIFO pull dispatch (`fifo_pull`) selectable via `MINICLOUD_DISPATCH_POLICY=fifo_pull` using `SELECT ... FOR UPDATE SKIP LOCKED` on eligible jobs with worker slot capacity checks.
- **Health and Readiness Probes:** Liveness check at `/healthz` and database connectivity probe at `/readyz` mapping connection failures to HTTP 503 `SERVICE_UNAVAILABLE`.
- **API Boundary Protections:** Worker ID and route parameter validation (`^[a-zA-Z0-9_-]+$`, max 128 chars), numeric bounds validation on attempts (1..20) and timeouts (1..86400s), and payload size limits (64 KiB).
- **Outage Resilience:** Worker client exponential backoff with jitter (200 ms base, 3 s cap) on HTTP 5xx or poll errors, and automatic worker re-registration upon coordinator restart.

Per-slice implementation status is in [docs/PROJECT_STATE.md](docs/PROJECT_STATE.md); later versions are in [docs/roadmap.md](docs/roadmap.md).

## Architecture

MiniCloud consists of four core executables and an authoritative PostgreSQL database:

- **Coordinator (`cmd/coordinator`):** Serves the HTTP API (`internal/api`), tracks worker registrations and heartbeats, validates transitions through the domain FSM (`internal/domain`), and orchestrates job dispatching.
- **Worker (`cmd/worker`):** Polls the coordinator for work, runs allowlisted tasks inside bounded execution slots (`internal/executor`), sends periodic heartbeats, and reports task results.
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
|   |   (in-memory repository or internal/storage/postgres)   |   |
|   +---------------------------------------------------------+   |
+-----------------------------------------------------------------+
          ^                              |
          | HTTP Poll / Heartbeat        | PostgreSQL 16
          | (/v1/workers/*)              | (deployments/docker-compose.yml)
          v                              v
+-----------------------+      +----------------------------------+
|     Worker Agent      |      |           PostgreSQL             |
|     (cmd/worker)      |      |   jobs, attempts, workers,       |
|                       |      |   results, job_events            |
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

Prerequisites: Go 1.22.2 and `make`. Run every command from the repository root. Terminals 1 and 2 stay in the foreground and log JSON; stop them with Ctrl-C when done.

Startup order: Start the coordinator first, wait until it is ready, then start the worker process, and finally submit jobs or inspect state with `mc`.

Port-busy advice: If port 8080 is busy (`ss -tulpn | grep 8080` or `ss -ltn | grep 8080`), the coordinator logs `address already in use` and exits. The worker and `mc` default to port 8080 and will silently talk to whatever foreign process is listening there. To use a different port (such as 18080), set `MINICLOUD_HTTP_ADDR=127.0.0.1:18080` when starting the coordinator, and set `MINICLOUD_COORDINATOR_URL=http://127.0.0.1:18080` for the worker and in your `mc` shell (`export MINICLOUD_COORDINATOR_URL=http://127.0.0.1:18080`). Note that while `mc submit` supports a `--coordinator-url` flag, other `mc` subcommands (`mc get`, `mc attempts`, `mc workers`) read `MINICLOUD_COORDINATOR_URL` from the environment.

Build all executables:

```bash
make build
```

Terminal 1: Start the coordinator in the foreground:

```bash
MINICLOUD_STORAGE_DRIVER=memory MINICLOUD_HTTP_ADDR=127.0.0.1:8080 ./bin/coordinator
```

Check first that nothing listens on the port (`ss -ltn | grep ':8080'` prints nothing). The coordinator logs `coordinator listening for requests` before it binds; if the next log line is `http server failed`, the port was busy. Wait until the coordinator is ready, or verify with the readiness probe:

```bash
until curl -sf http://127.0.0.1:8080/readyz >/dev/null; do sleep 0.5; done
```

Terminal 2: Start a worker process:

```bash
MINICLOUD_COORDINATOR_URL=http://127.0.0.1:8080 MINICLOUD_WORKER_ID=worker-1 MINICLOUD_WORKER_SLOTS=2 ./bin/worker
```

Wait for the worker to register (it logs `worker successfully registered with coordinator`) before running commands in Terminal 3.

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

Copy the `ID:` value from the submit output; the job ID is generated per submission and yours will differ from the one shown here. Wait about a second for the worker to pick the job up, then query it (repeat if `State` is still `QUEUED` or `RUNNING`):

```bash
./bin/mc get <job_id>
```

The IDs, attempt IDs and times in the outputs below come from one sample run.

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
./bin/mc attempts <job_id>
```

Output:
```text
Attempts for Job job_1_9fb43b921313 (1 total):
NO   ATTEMPT ID           WORKER     STATE       EXIT   STARTED
1    att_1_46ceaa3f05b8   worker-1   COMPLETED   0      11:47:14
```

### 2. PostgreSQL Mode (Durable Persistence)

*(Note: The commands in this subsection describe the PostgreSQL provisioning path and are labeled designed, not executed; checked by code reading only)*

1. Copy `.env.example` to `.env` and set local credentials:

```bash
cp .env.example .env
```

Validate the Docker Compose configuration (the `-q` flag validates syntax without printing resolved passwords to the terminal):

```bash
docker compose --env-file .env -f deployments/docker-compose.yml config -q
```

2. Start the PostgreSQL container:

```bash
make db-up
```

3. Run database migrations:

`.env` is read by `make` and `docker compose`, not by the binaries. Do not `source` it into your interactive shell: `.env` also defines `MINICLOUD_TEST_DATABASE_URL`, and a subsequent `go test ./...` in that shell would run outage tests that stop and restart the shared PostgreSQL container. Build the executables first if not already done (`make build`), then run migrations passing only the required database URL in a subshell:

```bash
( export MINICLOUD_DATABASE_URL="$(grep -E '^MINICLOUD_DATABASE_URL=' .env | cut -d= -f2-)"; ./bin/migrate )
```

4. Start the coordinator with the PostgreSQL storage driver:

```bash
( export MINICLOUD_DATABASE_URL="$(grep -E '^MINICLOUD_DATABASE_URL=' .env | cut -d= -f2-)"; MINICLOUD_STORAGE_DRIVER=postgres ./bin/coordinator )
```

5. Start workers and submit jobs as shown in the in-memory quick start.

## Configuration

All configuration is loaded from environment variables:

| Environment Variable | Default Value | Used By | Source File | Description |
|---|---|---|---|---|
| `MINICLOUD_HTTP_ADDR` | `127.0.0.1:8080` | Coordinator | `internal/config/config.go` | HTTP server listening address and port |
| `MINICLOUD_STORAGE_DRIVER` | `memory` | Coordinator | `internal/config/config.go` | Storage engine (`memory` or `postgres`) |
| `MINICLOUD_DATABASE_URL` | `""` | Coordinator, Migrator | `internal/config/config.go`, `cmd/migrate/main.go` | PostgreSQL database connection URL |
| `MINICLOUD_HEARTBEAT_INTERVAL` | `2s` | Worker (coordinator validates it against `MINICLOUD_WORKER_TIMEOUT`) | `internal/config/config.go` | Frequency of worker heartbeat transmissions |
| `MINICLOUD_WORKER_TIMEOUT` | `10s` | Coordinator | `internal/config/config.go` | Liveness window before an uncommunicative worker is declared dead (>= 3x heartbeat interval) |
| `MINICLOUD_REAPER_INTERVAL` | `5s` (`workerTimeout / 2`) | Coordinator | `internal/config/config.go` | Interval for coordinator worker liveness sweep |
| `MINICLOUD_DEFAULT_MAX_ATTEMPTS` | `3` | Coordinator | `internal/config/config.go` | Default maximum attempt count for jobs submitted without `max_attempts` (default 3, valid range 1..20) |
| `MINICLOUD_MAX_JOB_RUNTIME` | `60s` | Coordinator | `internal/config/config.go` | Parsed and validated at startup, but not enforced by any component. The worker applies the per-job `timeout_seconds` (1..86400) to task execution and clamps it to `[1, 86400]` seconds via `ClampExecutionTimeout` (SEC-012 fixed in Slice P4-09) |
| `MINICLOUD_LEASE_DURATION` | `30s` | Coordinator | `internal/config/config.go` | Attempt lease duration ([ADR-0014](docs/adr/0014-lease-model-and-expiry.md)) |
| `MINICLOUD_SWEEPER_INTERVAL` | `5s` | Coordinator | `internal/config/config.go` | Interval for coordinator attempt lease sweeper passes |
| `MINICLOUD_STARTUP_GRACE_PERIOD` | `30s` | Coordinator | `internal/config/config.go` | Reaping suppression window after coordinator start (grace values must be at least the lease duration) |
| `MINICLOUD_OUTAGE_GRACE_WINDOW` | `30s` | Coordinator | `internal/config/config.go` | Reaping suppression and cooldown window following a database outage (grace values must be at least the lease duration) |
| `MINICLOUD_DISPATCH_POLICY` | `round_robin` | Coordinator | `internal/config/config.go` | Dispatch policy selection (`round_robin` default since P4-17; `fifo_pull` selectable) |
| `MINICLOUD_ASSIGNMENT_ACCEPT_WINDOW` | `5s` | Coordinator | `internal/config/config.go` | Assignment accept window before reservation lapses (valid range 5s..30s) |
| `MINICLOUD_ASSIGNMENT_INTERVAL` | `1s` | Coordinator | `internal/config/config.go` | Interval for assignment pass loop (valid range 100ms..10s, must not exceed accept window) |
| `MINICLOUD_ASSIGNMENT_MAX_LAPSES` | `3` | Coordinator | `internal/config/config.go` | Maximum assignment lapses before job falls back to pull dispatch (valid range 1..10) |
| `MINICLOUD_ASSIGNMENT_FALLBACK_AGE` | `30s` | Coordinator | `internal/config/config.go` | Job age threshold for age-ordered fallback claims (valid range: at least `MAX_LAPSES * (ACCEPT_WINDOW + INTERVAL)` up to 10m) |
| `MINICLOUD_ASSIGNMENT_BATCH_LIMIT` | `50` | Coordinator | `internal/config/config.go` | Maximum jobs reserved per assignment pass (valid range 1..500) |
| `MINICLOUD_ASSIGNMENT_MIN_YIELD` | `10ms` | Coordinator | `internal/config/config.go` | Minimum yield sleep when assignment pass makes progress (valid range 1ms..1s, must not exceed assignment interval) |
| `MINICLOUD_LOG_LEVEL` | `info` | Coordinator, Worker | `internal/config/config.go` | Parsed, but not applied; logging is fixed at `info` |
| `MINICLOUD_DB_MAX_CONNS` | `20` | Coordinator | `internal/config/config.go` | Maximum connections in the PostgreSQL pool |
| `MINICLOUD_DB_MIN_CONNS` | `2` | Coordinator | `internal/config/config.go` | Minimum idle connections in the PostgreSQL pool |
| `MINICLOUD_DB_QUERY_TIMEOUT` | `5s` | Coordinator | `internal/config/config.go` | Boot timeout, and `statement_timeout`/`lock_timeout` for pool connections |
| `MINICLOUD_COORDINATOR_URL` | `http://127.0.0.1:8080` | Worker, CLI (`mc`) | `internal/config/config.go`, `cmd/mc/main.go` | Base URL of the coordinator service |
| `MINICLOUD_WORKER_ID` | `""` (auto-generated) | Worker | `internal/config/config.go`, `cmd/worker/main.go` | Identifier for the worker (defaults to `worker_<hostname>_<pid>`) |
| `MINICLOUD_WORKER_SLOTS` | `4` | Worker | `internal/config/config.go` | Concurrency execution capacity of the worker |
| `MINICLOUD_TEST_DATABASE_URL` | `""` | Test Suites | `internal/storage/postgres/testhelper_test.go` | Isolated database URL for PostgreSQL integration tests (DB name must end in `_test`) |

Malformed or unparseable numeric or duration values stop service startup with an error naming the variable; unset or empty values take their defaults. Coordinator validation requires `MINICLOUD_DEFAULT_MAX_ATTEMPTS` to be between 1 and 20, and grace values (`MINICLOUD_STARTUP_GRACE_PERIOD` and `MINICLOUD_OUTAGE_GRACE_WINDOW`) to be at least the lease duration (`MINICLOUD_LEASE_DURATION`).

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
- `mc submit --task <type> --payload <json> [--timeout <sec>] [--max-attempts <num>]`: Submits a job to the coordinator (`--coordinator-url` overrides `MINICLOUD_COORDINATOR_URL`). Allowlisted task types are `echo`, `sleep`, and `sha256_loop`.
- `mc get <job_id>`: Displays job status, current attempt ID, timestamps, and execution output.
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
- `POST /v1/jobs/{id}/cancel` - Request cancellation. A queued job is cancelled immediately. For a running job the coordinator records `CANCEL_REQUESTED`; the worker learns of it through the lease renewal response ([ADR-0016](docs/adr/0016-cancellation-semantics.md), [docs/protocol.md](docs/protocol.md)). Returns HTTP 200.

### Worker Protocol
- `POST /v1/workers/register` - Register a worker, its capacity slots, and supported task types.
- `POST /v1/workers/{id}/heartbeat` - Send periodic liveness ping and report available capacity slots.
- `POST /v1/workers/{id}/poll` - Poll for next available job assignment (claims assigned work under default `round_robin` or pulls work under selectable `fifo_pull`).
- `POST /v1/jobs/{id}/attempts/{attempt_id}/result` - Report completion or failure of an assigned attempt.
- `POST /v1/jobs/{id}/attempts/{attempt_id}/renew` - Renew an active attempt lease and receive cancellation notifications.
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

- `make all`: Runs `fmt`, `vet`, `build`, and `test-race`.
- `make build`: Compiles all binaries into `bin/` (`coordinator`, `worker`, `mc`, `migrate`).
- `make run-coordinator`: Compiles and runs the coordinator binary in the foreground.
- `make run-worker`: Compiles and runs the worker binary in the foreground.
- `make test`: Runs standard unit and integration tests with package execution serialized (`go test -v -p 1 ./...`).
- `make test-race`: Runs the test suite with the Go race detector enabled and package execution serialized (`go test -v -race -p 1 ./...`).
- `make test-docs`: Verifies documentation citations, links, and rules via `tests/docs`.
- `make test-security`: Runs the 72-test security and adversarial test suite (`tests/faultinject` and `tests/abuse`) using `-p 1` against a dedicated per-agent test database; `-p 1` is required because the assignment pass lock is database-wide.
- `make test-db`: Runs the PostgreSQL-backed tests in `internal/storage/postgres` and `internal/coordinator`, plus one coordinator-restart integration test in `tests/integration`, using the `MINICLOUD_TEST_DATABASE_URL` value read from `.env`.
- `make fmt`: Automatically formats Go source code using `gofmt -s -w .`.
- `make vet`: Executes Go static code analysis (`go vet ./...`).
- `make demo`: Runs the automated multi-process local demo script (`scripts/demo.sh`).
- `make clean`: Removes compiled binaries from `bin/`.
- `make db-up`: Starts the PostgreSQL container via Docker Compose and waits for healthy status.
- `make db-down`: Stops and removes the shared PostgreSQL container and network; every dependent test run loses its database (named volumes preserved).
- `make db-psql`: Opens an interactive `psql` console in the database container.
- `make db-reset-test`: Drops and recreates the shared `minicloud_test` database (the name is hardcoded, so it does not touch other databases).

### Test Guardrails and Warnings

- **Test Database Isolation:** All tests requiring PostgreSQL verify that the target database name ends strictly with `_test` (checked in `internal/storage/postgres/testhelper_test.go`). If `MINICLOUD_TEST_DATABASE_URL` is unset, database tests skip cleanly.
- **Container Interruption Warning:** With `MINICLOUD_TEST_DATABASE_URL` set in your environment, `go test ./...` (and therefore `make test` and `make test-race`) runs outage tests (`tests/integration/postgres_wiring_test.go`, `tests/integration/part_d_security_test.go`) that run `docker compose stop postgres` and `start postgres` on the shared `minicloud-postgres` container. Anything else using that database is disconnected meanwhile. Never run `go test ./...` with the database URL set in the shell; use `make test-db`. For database tests use `make test-db`, which reads the URL from `.env` and runs a fixed, narrow set (see `make test-db` in the Makefile). To run everything else, unset the variable: `env -u MINICLOUD_TEST_DATABASE_URL go test ./...`. Database tests skip when it is unset; a skipped test is not a passed test.

## Roadmap

MiniCloud development follows a three-version roadmap defined in [docs/roadmap.md](docs/roadmap.md):

- **V1 (Phases 0–4):** Core execution with allowlisted tasks, transactional dispatch (`round_robin` default since P4-17; `fifo_pull` selectable), PostgreSQL durability, lease management, bounded retries, cancellation reaching running workers, and post-Phase 4 assignment-based round-robin scheduling.
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
