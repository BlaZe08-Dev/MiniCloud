# Documentation Accuracy and Consistency Audit (DOC-AUDIT-1)

- **Audit Date:** 2026-11-19
- **Branch Audited:** `agent/gemini/DOC-AUDIT-1` (branched from `origin/main` at `26b440a`)
- **Commit Audited:** `26b440a8e6c358f76a0f306925a51305f10b8e90`
- **Agent:** Gemini (Antigravity)
- **Database Used:** `minicloud_gemini_test` (per-worktree database, `minicloud_test` never touched)
- **Scope:** Read-only audit across all documentation, task boards, ADRs, specifications, and code contracts.

---

## 1. Executive Summary & Verdict

- **Verdict:** `MINOR FIXES`
- **Summary:** The core architecture, protocol specifications, database schema, security limits, and Quick Start guides are in extraordinarily good shape. The README Quick Start executes cleanly and successfully both in In-Memory mode and PostgreSQL mode (`minicloud_gemini_test`), as well as in a fresh clone outside the workspace. No broken links or out-of-range line citations exist in any live documentation. All 11 `[Decided]` claims in `docs/post-v1/` have concrete citations to accepted ADRs, domain invariants, or released Go code.
- **Identified Drift:** Drift is limited to status synchronization on task tracking tables following recent merges (PV-02 in PR #59, V1x-02 in PRs #60/#61, V2-0 in PRs #57/#58), minor error string wording discrepancies in `field-limits.md`, one missing column in the README CLI output sample, and an obsolete comment regarding HTML escaping in `internal/worker/client.go`.
- **Grouping Recommendation:** Group all 13 findings into a single docs task (e.g., `DOC-SYNC-1`) with no code changes needed.

---

## 2. Table of Findings

| ID | Severity | Location | What Document Says | What Evidence / Code Shows | Evidence Label | Smallest Proposed Fix |
|---|---|---|---|---|---|---|
| **DA-1** | Low | `docs/agents/TASKS.md:26,62`, `docs/PROJECT_STATE.md:9` | PV-02 is "complete on branch, awaiting owner merge". | Merged to `main` in PR #59 (`140be14`, 2026-11-18). | confirmed by code reading | Move PV-02 row to Done table in `TASKS.md` citing PR #59; update `PROJECT_STATE.md`. |
| **DA-2** | Low | `docs/agents/TASKS.md:28,74`, `docs/PROJECT_STATE.md:8` | V1x-02 is "built, awaiting audit". | Merged to `main` in PR #60 (`73c21cd`) and PR #61 (`26b440a`). | confirmed by code reading | Move V1x-02 to Done table in `TASKS.md` citing PR #60/#61; update `PROJECT_STATE.md`. |
| **DA-3** | Low | `docs/agents/TASKS.md:29,87`, `docs/PROJECT_STATE.md:7`, `docs/roadmap.md:29`, `docs/post-v1/ROADMAP.md:3,17` | V2-0 is "audited, fix round 1"; roadmap says "planned, not started / none is implemented". | V2-0 implementation was merged in PR #57 (`450aac7`) and fix round 1 audit in PR #58 (`28f5c61`). Concurrency is live. | confirmed by code reading | Update `TASKS.md`, `PROJECT_STATE.md`, and roadmaps to reflect V2-0 is merged; note V2-0b is the active task. |
| **DA-4** | Minor | `docs/agents/TASKS.md:27` | O1-check is in Active tasks table. | Completed and merged to `main` in PR #56 (`7893aab`). | confirmed by code reading | Move O1-check row to Done table in `TASKS.md`. |
| **DA-5** | Minor | `docs/agents/TASKS.md:32-52` | 21 closed Phase 4 tasks (`P4-19` to `P4-06b`) remain in the "Active and planned tasks" table. | These tasks were merged in PRs #31–#51 before V1 release and already exist in the Done table (`TASKS.md:316-337`). | confirmed by code reading | Delete the 21 duplicate rows from the Active table in `TASKS.md`. |
| **DA-6** | Minor | `docs/PROJECT_STATE.md:20` | Section header reads "Target Release: MiniCloud V1...". | V1 is released (`v1.0.0` on `166c719`). | confirmed by code reading | Update line 20 header to "Release: MiniCloud V1 (`v1.0.0` released 2026-11-15)". |
| **DA-7** | Low | `docs/security/field-limits.md:67-68` | Check failure errors are `"payload size exceeds maximum allowed 65536 bytes"` and `"output size exceeds maximum allowed 65536 bytes"`. | Code returns `domain.ErrPayloadTooLarge` (`"payload exceeds maximum allowed size"`) and `domain.ErrOutputTooLarge` (`"output exceeds maximum allowed size"`). | confirmed by code reading | Update error message strings in `field-limits.md:67-68` to match `internal/domain/errors.go:15-16`. |
| **DA-8** | Minor | `docs/security/field-limits.md:73` | Error message reads `"supported_tasks cannot exceed 16 tasks"`. | Code formats as `"supported_tasks must contain at most 16 tasks"` in `internal/storage/postgres/repository.go:1613`. | confirmed by code reading | Align wording in `field-limits.md` to `"must contain at most 16 tasks"`. |
| **DA-9** | Minor | `CHANGELOG.md:70` | Cites migration filename `000004_phase4_coordination.sql`. | Migration on disk is named `000004_leases_retries_cancellation.sql`. | confirmed by code reading | Correct migration filename in `CHANGELOG.md:70`. |
| **DA-10** | Minor | `README.md:178` | `./bin/mc attempts` sample output header has columns `NO ATTEMPT ID WORKER STATE EXIT STARTED`. | CLI command output includes `CLASS` column: `NO ATTEMPT ID WORKER STATE CLASS EXIT STARTED`. | executed | Add `CLASS` column to the sample output in `README.md:178`. |
| **DA-11** | Minor | `docs/adr/0001-record-architecture-decisions.md`, `docs/adr/0002-go-stdlib-http-for-initial-api.md` | ADR 0001 and ADR 0002 lack a `## Status` section. | ADRs 0003 through 0019 all have an explicit `## Status` header (e.g. `## Status\n\nAccepted`). | confirmed by code reading | Add `## Status\n\nAccepted` to ADR 0001 and 0002. |
| **DA-12** | Minor | `docs/PROJECT_STATE.md:5` | Cites V1 code baseline test gate counts as 401/91/0. | Current `main` gate count is 421/91/0 (+20 tests added in V2-0 and V1x-02). | executed | Add a note documenting the current test suite baseline (421/91/0, db 163/0/0, sec 75/0/0). |
| **DA-13** | Minor | `internal/worker/client.go:46-52` | Comment explains `MaxPollResponseBytes` headroom citing coordinator HTML entity escaping. | SEC-067 (PR #60, V1x-02) disabled HTML escaping in coordinator `writeJSON` (`SetEscapeHTML(false)`). | confirmed by code reading | Update comment in `client.go` to reflect SEC-067 HTML escaping removal. |

---

## 3. Detailed Audit by Check

### Check 1: Status Claims vs Git History
- **Script:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check1_status_claims.py.txt` (executed)
- **Verification:** Ran `git log --merges --oneline -74` and checked all PR merges from PR #31 to PR #61.
- **Findings:**
  - `PV-02`: Marked awaiting merge in `TASKS.md` (lines 26, 62) and `PROJECT_STATE.md` (line 9). Merged in PR #59 (`140be14`).
  - `V1x-02`: Marked awaiting audit in `TASKS.md` (lines 28, 74) and `PROJECT_STATE.md` (line 8). Merged in PR #60 (`73c21cd`) and PR #61 (`26b440a`).
  - `V2-0`: Marked awaiting fix round 1 in `TASKS.md` (lines 29, 87) and `PROJECT_STATE.md` (line 7). Merged in PR #57 (`450aac7`) and PR #58 (`28f5c61`). Concurrency is fully integrated into the worker agent and coordinator.
  - `O1-check`: Present in Active table in `TASKS.md` (line 27). Merged in PR #56 (`7893aab`).
  - Active table in `TASKS.md`: Contains 21 stale rows for Phase 4 tasks (`P4-19` to `P4-06b`) that were completed and merged before V1.0.0 tag, and already appear in the Done table.
  - `PROJECT_STATE.md:20`: Header retains "Target Release: MiniCloud V1" despite V1 release.

### Check 2: Facts vs Code
- **Script:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check2_facts_vs_code.py.txt` (executed)
- **Config & Limit Verification:**
  - Ports: Coordinator default `127.0.0.1:8080` matches `internal/config/config.go:42` and `README.md:9,89`. Postgres container `127.0.0.1:5433` matches `deployments/docker-compose.yml:6` and `docs/testing-postgres.md`.
  - Field Limits:
    - Max payload: 65,536 bytes (`internal/domain/job.go:12`).
    - Max output: 65,536 bytes (`internal/domain/job.go:13`).
    - Worker ID pattern: `^[a-zA-Z0-9_-]+$`, max 128 (`internal/domain/assignment.go:11`, `internal/api/handlers.go:27`).
    - Worker supported tasks max: 16 (`internal/domain/worker.go:12`).
    - Worker slots: default 4, min 1, max 1000 (`internal/config/config.go:49`, `internal/worker/client.go:27`).
    - Lease duration: default 30s, min 5s, max 300s (`internal/config/config.go:47`, `internal/domain/lease.go`).
    - Heartbeat interval: default 5s (`internal/config/config.go:48`).
    - Sweeper interval: default 5s (`internal/config/config.go:50`).
  - Mismatches Found:
    - Error string in `field-limits.md:67`: Doc states `"payload size exceeds maximum allowed 65536 bytes"`, code has `domain.ErrPayloadTooLarge` (`"payload exceeds maximum allowed size"`).
    - Error string in `field-limits.md:68`: Doc states `"output size exceeds maximum allowed 65536 bytes"`, code has `domain.ErrOutputTooLarge` (`"output exceeds maximum allowed size"`).
    - Error string in `field-limits.md:73`: Doc states `"supported_tasks cannot exceed 16 tasks"`, code formats as `"supported_tasks must contain at most 16 tasks"` (`internal/storage/postgres/repository.go:1613`).
    - Migration name in `CHANGELOG.md:70`: `000004_phase4_coordination.sql` vs actual `000004_leases_retries_cancellation.sql`.

### Check 3: Cross-Doc Contradictions & Sequence Integrity
- **Script:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check3_numbers_and_crossdoc.py.txt` (executed)
- **ADR Audit:** ADR 0001 to 0019 all exist with sequential numbers. ADR 0001 and ADR 0002 lack a `## Status` header, while ADRs 0003–0019 all have `## Status` (DA-11).
- **Open Decisions (OD):** Exactly 25 decisions referenced in `docs/post-v1/`. All 25 (`OD-01` through `OD-25`) are defined sequentially in `docs/post-v1/OPEN_DECISIONS.md`. Zero missing, zero duplicates.
- **Security Findings (SEC):** Exactly 67 findings tracked (`SEC-001` through `SEC-067`) in `docs/security/findings.md` register table. Zero missing numbers, zero duplicates.
- **Slice Names:** Slice naming is consistent across `TASKS.md`, `roadmap.md`, and `post-v1/ROADMAP.md` (`P4-*`, `PV-*`, `V1x-*`, `V2-*`).
- **Migrations:** Next migration is consistently stated as `000006` across all docs (`TASKS.md:15`, `roadmap.md:37`, `post-v1/ROADMAP.md:18`, `CURRENT_BASELINE.md:100`), with `000001` to `000005` applied.

### Check 4: Link & Reference Integrity
- **Scripts:**
  - `docs/agents/reviews/DOC-AUDIT-1-evidence/check4_links_citations.py.txt` (executed)
  - `docs/agents/reviews/DOC-AUDIT-1-evidence/check4_extract_and_verify.py.txt` (executed)
  - Evidence file: `docs/agents/reviews/DOC-AUDIT-1-evidence/check4_verified_45_citations.txt`
- **Markdown Links:** Checked 178 relative markdown links across all docs. In all live documentation (`README.md`, `AGENTS.md`, `CHANGELOG.md`, `docs/*.md`, `docs/post-v1/*.md`, `docs/adr/*.md`, `docs/security/*.md`), zero broken links found. (7 relative link targets with missing `../../` prefixes were found strictly in frozen review records `P0-03-round2.md` and `P0-03-round3.md`).
- **File & Line Citations:** Over 1,300 citations checked. Zero missing target files and zero out-of-range line citations in live docs.
- **Spot-Check (45 Citations):**
  - Sampled 45 concrete citations across Core Architecture, Protocol, State Machine, Schema, Security Findings, Threat Model, Field Limits, ADRs (0014, 0015, 0016, 0017, 0019), and Post-V1 specifications.
  - Result: 45 / 45 citations verified against code on branch (all point to existing files, valid line bounds, and corresponding content).
- **Environment Variables:** All 31 environment variables in Go code are documented. 10 documentation occurrences refer to historical or pattern names (e.g. `MINICLOUD_ASSIGNMENT_` prefix, doc-internal test aids).
- **Test Citations:** All cited test names exist in the codebase or are documented in `tests/docs/citation_allowlist.txt`. `make test-docs` passes cleanly (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 3.531s`).

### Check 5: `[Decided]` vs `Proposed` in Post-V1 Docs
- **Script:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check5_decided_vs_proposed.py.txt` (executed)
- **Analysis:**
  - Found 11 lines containing `[Decided]` across `docs/post-v1/`.
  - Every single `[Decided]` label cites an accepted ADR (ADR-0015, ADR-0017, ADR-0018), an `AGENTS.md` invariant (Invariant 4, 5, 7), an approved owner decision (D1, D2, D3), or merged code (`internal/worker/client.go:74`, `internal/storage/postgres/assignment.go:387,476,578`, `internal/storage/postgres/repository.go:848`).
  - Found 30 occurrences of `Proposed` / `[Proposed]`. None of these proposed designs is claimed as currently implemented in `README.md`, `docs/roadmap.md`, or `docs/PROJECT_STATE.md`. All are clearly qualified with "planned, not started / none is implemented".

### Check 6: README Quick Start Literal Execution
- **Script:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check6_quickstart_runner.py.txt` (executed)
- **Evidence Output:** `docs/agents/reviews/DOC-AUDIT-1-evidence/check6_quickstart_execution.txt`
- **Results:**
  1. **In-Memory Mode:**
     - `make build` -> binary creation succeeded.
     - `MINICLOUD_STORAGE_DRIVER=memory MINICLOUD_HTTP_ADDR=127.0.0.1:18080 ./bin/coordinator` -> bound cleanly, readiness probe `/readyz` returned 200 OK.
     - `MINICLOUD_COORDINATOR_URL=... ./bin/worker` -> worker registered with 2 slots.
     - `./bin/mc workers` -> reported worker healthy (2/2 slots).
     - `./bin/mc submit --task echo --payload '{"message":"hello minicloud"}'` -> job queued (`job_1_3ab2e55f495a`).
     - `./bin/mc get` -> job state transitioned to `SUCCEEDED` with exit code 0.
     - `./bin/mc attempts` -> showed attempt completed with exit code 0.
  2. **PostgreSQL Mode (`minicloud_gemini_test`):**
     - Reset database `minicloud_gemini_test` safely per AGENTS.md section 9.
     - `./bin/migrate` -> applied 5 migrations successfully.
     - Coordinator started with PostgreSQL driver -> readiness returned 200 OK.
     - Worker registered, submitted echo job, execution succeeded with exit code 0.
  3. **Fresh Clone Outside Repository:**
     - Cloned to `/home/blazeee/mc-quickstart-audit`.
     - `make build` succeeded.
     - Ran coordinator, worker, submitted job with `mc` -> job succeeded with exit code 0.
     - Cleaned up clone directory.
- **Finding:** In `./bin/mc attempts` sample output, `README.md:178` shows `NO ATTEMPT ID WORKER STATE EXIT STARTED`, but actual CLI output includes the `CLASS` column: `NO ATTEMPT ID WORKER STATE CLASS EXIT STARTED` (DA-10).

### Check 7: Baselines Audit
- **Unset Test Suite:**
  - Command: `env -u MINICLOUD_TEST_DATABASE_URL go test -race -count=1 ./...`
  - Current Count: `PASS 421, SKIP 91, FAIL 0`
  - Quoted in `docs/PROJECT_STATE.md:5`: Baseline `d7a397c` was `401/91/0`. The count has grown by +20 tests from V2-0 (concurrency) and V1x-02 (SEC-067 JSON escaping tests).
- **PostgreSQL Database Suite (`make test-db`):**
  - Command: `make test-db` (on `minicloud_gemini_test`)
  - Current Count: `PASS 163, SKIP 0, FAIL 0`
  - Quoted in docs: `163/0/0` (matches exactly).
- **Security Suite (`make test-security`):**
  - Command: `make test-security`
  - Current Count: `PASS 75, SKIP 0, FAIL 0`
  - Quoted in docs: `75/0/0` (matches exactly).
- **Documentation Suite (`make test-docs`):**
  - Command: `make test-docs`
  - Result: `PASS` (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 3.531s`).

---

## 4. Quality Gates Verification

- `test -z "$(gofmt -l .)"`: PASS (no unformatted Go files)
- `go vet ./...`: PASS (no vet issues)
- `go build ./...`: PASS (all packages build cleanly)
- `make test-docs`: PASS (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 3.531s`)
- Database preflight: PASS (`minicloud_gemini_test`)
- Test suites on current main:
  - `unset suite`: PASS 421, SKIP 91, FAIL 0
  - `make test-db`: PASS 163, SKIP 0, FAIL 0
  - `make test-security`: PASS 75, SKIP 0, FAIL 0
