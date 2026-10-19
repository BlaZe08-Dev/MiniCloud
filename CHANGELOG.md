# Changelog

All notable changes to MiniCloud will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- **Phase 0 Context & Architecture:**
  - `GEMINI.md`: Repository operating manual, invariant list, and session protocol.
  - `docs/PROJECT_STATE.md`: Living handoff state and verified command log.
  - `docs/architecture.md`: System components, boundaries, data flow, and ownership rules.
  - `docs/state-machine.md`: Finite state machine, transition matrix, and attempt lifecycle.
  - `docs/failure-model.md`: Fault scenarios, expected behaviors, and testing matrix.
  - `docs/protocol.md`: Client HTTP API and worker pull-based coordination protocol.
  - Architecture Decision Records:
    - `docs/adr/0001-record-architecture-decisions.md`
    - `docs/adr/0002-go-stdlib-http-for-initial-api.md`
    - `docs/adr/0003-pull-based-worker-protocol.md`
  - `.gitignore` for Go binaries, test artifacts, and IDE configurations.
