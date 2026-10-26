# MiniCloud

MiniCloud is a learning-oriented but engineering-serious distributed job execution platform written in Go. It accepts computational jobs, schedules attempts across independent worker processes, validates lifecycle transitions through a central state machine, enforces attempt leases, handles partial failures, and records structured execution results.

The objective is to understand, measure, and implement real distributed systems engineering: explicit state transitions, worker coordination, failure detection, bounded retries, and reproducible benchmarks.

---

## Current Status
- **Phase:** Phase 0 (Design & Context Layer Complete) / Entering Milestone 1 ("Local Core").
- **Current Branch:** `main` (for docs) -> `milestone/1-local-core` (for implementation).

---

## Prerequisites
- **Operating System:** Linux (developed on Linux Mint 22.3 / Ubuntu Noble).
- **Go:** Go 1.22+ (`go version go1.22.2 linux/amd64` verified).
- **Tools:** `git`, `make`, `curl`.

---

## Quick Start & Verification

### Build Binaries
```bash
make build
```
Builds the following executables into `bin/`:
- `bin/coordinator`: Central control plane daemon.
- `bin/worker`: Independent worker agent process.
- `bin/mc`: Command-line interface for operators and developers.

### Run Tests
```bash
make test-race
make test-docs
```
Executes all unit and integration tests with the Go race detector enabled, and verifies documentation citation consistency.

### Run Local Demo
```bash
make demo
```
Starts the coordinator and 2 independent worker processes in background, submits jobs via `mc`, queries attempts, and waits for successful completion.

---

## Repository Layout
```text
minicloud/
├── .gitignore
├── CHANGELOG.md             # Version history
├── GEMINI.md                # AI/Engineer operating manual & session handbook
├── Makefile                 # Standard build and test targets
├── README.md                # Project overview and instructions
├── cmd/
│   ├── coordinator/         # Coordinator daemon entry point
│   ├── worker/              # Worker agent daemon entry point
│   └── mc/                  # Developer CLI (submit, get, attempts, workers)
├── docs/
│   ├── PROJECT_STATE.md     # Living handoff file and milestone progress
│   ├── architecture.md      # Components, boundaries, and data flow
│   ├── state-machine.md     # FSM, transition matrix, and attempt lifecycle
│   ├── failure-model.md     # Fault matrix and expected behaviors
│   ├── protocol.md          # HTTP API & worker protocol specification
│   ├── adr/                 # Architecture Decision Records
│   └── source/              # Blueprint PDF and instructions
├── internal/
│   ├── api/                 # HTTP handlers and request validation
│   ├── config/              # Environment variable parsing and validation
│   ├── coordinator/         # Coordinator service and attempt tracking
│   ├── domain/              # Entities (Job, Attempt, Worker) and central FSM
│   ├── executor/            # Task interface, allowlist registry, resource limits
│   ├── scheduler/           # Deterministic round-robin scheduler
│   ├── storage/             # In-memory repository interface and implementation
│   └── worker/              # Worker agent client loop
└── tests/
    └── integration/         # Multi-process end-to-end integration tests
```

---

## Documentation Links
- [System Architecture](docs/architecture.md)
- [State Machine & Transitions](docs/state-machine.md)
- [Failure Model & Fault Matrix](docs/failure-model.md)
- [Protocol Specification](docs/protocol.md)
- [Architecture Decision Records (ADRs)](docs/adr/)
- [Project Blueprint (Technical Source of Truth)](docs/source/MiniCloud_Project_Blueprint.pdf)
- [Engineering Instructions](docs/source/MINICLOUD_INSTRUCTIONS.md)
- [Session Operating Handbook](GEMINI.md)
