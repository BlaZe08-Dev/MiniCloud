Task: V2-1c-AUDIT, independent read-only audit of slice V2-1c (ADR-0022 node API, coordinator, config and docs)
Branch: agent/gemini/V2-1c-audit (from origin/agent/claude/V2-1c 20ce610)
Work tip (last commit before this report): 20ce610
Date: 2026-11-21

# V2-1c audit report

Auditor: Gemini in Antigravity. Worktree: `~/MiniCloud-gemini`, Database: `minicloud_gemini_test`.
Commit audited: `20ce61033a04baf1b8a5c3f60dc26d3ffabf365b` (builder tip).
No production code or tests on the branch under audit were changed. This report is the only commit.

Evidence labels (AGENTS.md section 2):
- **executed**: run and output captured directly.
- **confirmed by code reading**: verified by reading cited files and lines.
- **inferred**: logical deduction based on verified facts.

---

## 1. Verdict

**Merge after a fix round.**

### Reasons:
1. **Major defect in `RecordNodeTelemetry` (`internal/coordinator/nodes.go:83-88`)**:
   `Coordinator.RecordNodeTelemetry` invokes `GetNode` on every incoming report solely to verify that `sample_age_ms` does not place `sampled_at` prior to `node.CreatedAt`.
   - **Performance collapse on PostgreSQL**: `GetNode` performs a `REPEATABLE READ` read-only transaction with 5 separate queries (loading full node view, hardware history, telemetry, and workers), followed by a second read-write transaction with 2 queries (`SELECT ... FOR KEY SHARE` and the conditional upsert). Total: 2 transactions, 7 SQL statements per single telemetry report. This directly violates ADR-0022 Section 8.3 ("One conditional upsert on node_telemetry only").
   - **Severe lock contention in memory**: `GetNode` scans all registered workers (`O(workers)`) under the global repository read lock. Measured at 10,000 workers: 100 reports took 600.45 ms (~6.00 ms per report under lock).
   - **False rejections under clock skew**: `RecordNodeTelemetry` compares the coordinator clock (`s.clock.Now() - sample_age_ms`) against the database clock (`node.CreatedAt`). When coordinator clock lags database clock by even a slight delta (e.g. 1 s), perfectly valid fresh reports (`sample_age_ms: 0`) are falsely rejected with `400 INVALID_REQUEST: sample_age_ms puts sampled_at before the node was created`.
   - **Recommended fix**: Remove `GetNode` from `RecordNodeTelemetry`. Enforce `sampled_at >= created_at` inside the repository: on PostgreSQL, evaluate it in the database clock inside the existing query (`SELECT created_at FROM nodes WHERE id = $1 FOR KEY SHARE` or inside the upsert `INSERT ... ON CONFLICT`); in memory, perform a fast direct node existence lookup without loading workers or hardware.

2. All other parts of V2-1c (local-only enforcement, raw JSON worker registration remedying A-01, sensitive field masking, ceiling enforcement, and query parameter validation) are cleanly implemented, thoroughly tested, and match the specification.

---

## 2. Findings

| ID | Sev | file:line | What is wrong | Impact | Evidence | Recommended fix |
|---|---|---|---|---|---|---|
| F-01 | Major | `internal/coordinator/nodes.go:83-88` | `RecordNodeTelemetry` calls `s.repo.GetNode` on every telemetry report to check `sampledAt.Before(node.CreatedAt)` using the coordinator clock. | 1) Violates ADR 8.3: costs 2 transactions and 7 SQL queries per report on PostgreSQL; 2) O(workers) scan in memory stalls lock for ~6ms per report at 10k workers; 3) Clock skew causes false rejections (age 0ms fails when coordinator clock lags DB clock). | **executed**: probe showed 100 reports took 600.45ms in memory with 10k workers; clock skew of 1s behind DB caused 100% rejection with 400 `INVALID_REQUEST`. | Remove `GetNode` from coordinator; check `sampled_at >= created_at` in the database clock inside `RecordNodeTelemetry` storage method. |
| F-02 | Info | `internal/domain/node.go:50-58`, `internal/api/nodes.go:124-129` | Paging `after` parameter requires strictly lowercase UUID (`domain.ValidateNodeID`). Uppercase hex letters return 400 `INVALID_REQUEST`. | Not a defect: strictly matches RFC 9562 canonical format and ADR 9. Documented for client developers. | **executed**: `after=3A00...` returned 400 `INVALID_REQUEST ("after must be a canonical node id")`. | Keep as is; documented in `docs/security/field-limits.md`. |

---

## 3. Audit Tables

### Table 1: Local-Only Rule (ADR 4.4, Point 1)
Evaluated via `config.IsLocalOnlyAddr(addr)`. Decided once at coordinator startup, never per request, never from headers, no override.

| Input Address | `IsLocalOnlyAddr` Output | Matches ADR 4.4? | Rationale / Rule |
|---|---|---|---|
| `127.0.0.1:8080` | `true` | Yes | IPv4 loopback (127.0.0.0/8) with port |
| `127.0.0.2:1` | `true` | Yes | IPv4 loopback (127.0.0.0/8) with port |
| `[::1]:8080` | `true` | Yes | IPv6 loopback (`::1`) with port |
| `127.1.2.3:80` | `true` | Yes | IPv4 loopback (127.0.0.0/8) |
| `[::1]:80` | `true` | Yes | IPv6 loopback (`::1`) |
| `[::ffff:127.0.0.1]:80` | `true` | Yes | IPv4-mapped IPv6 loopback (`::ffff:127.0.0.1`) |
| `127.0.0.1` | `false` | Yes | Missing port (`net.SplitHostPort` fails closed) |
| `[fe80::1%lo]:80` | `false` | Yes | IPv6 link-local unicast (`fe80::/10`), not loopback |
| `0:80` | `false` | Yes | Host "0" parses to 0.0.0.0 (non-loopback) |
| `localhost.:80` | `false` | Yes | FQDN host name, unresolved at startup |
| `0.0.0.0:8080` | `false` | Yes | Non-loopback bind address |
| `:8080` | `false` | Yes | Empty host part |
| `[::]:8080` | `false` | Yes | IPv6 unspecified address |
| `localhost:8080` | `false` | Yes | Host name, unresolved at startup |
| `192.168.1.5:8080` | `false` | Yes | Private LAN IPv4 |
| `example.com:8080` | `false` | Yes | Domain name |
| `not an address` | `false` | Yes | Unparsable |
| `""` | `false` | Yes | Empty string |

Server constructors: `grep NewServer` shows `cmd/coordinator/main.go:205` is the only production caller, passing `api.WithLocalOnly(cfg.NodeLocalOnly)`. All test constructors default `localOnly` to `false` unless explicitly configured.

---

### Table 2: Worker Register (Finding A-01, Point 2)
Compared against v1.1.0 tag (`128110a`, commit `b55a1d8`) in a fresh throwaway worktree:

| Input `node_id` payload | v1.1.0 Response | V2-1c Not Served (`localOnly=false`) | V2-1c Served (`localOnly=true`) |
|---|---|---|---|
| absent | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` |
| `null` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` |
| `5` (number) | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("node_id must be a string") |
| `true` (bool) | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("node_id must be a string") |
| `{"a":1}` (object) | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("node_id must be a string") |
| unregistered UUID | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("node_id names no registered node") |
| `"not-a-uuid"` (malformed) | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("invalid node_id") |
| string with NUL byte | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("invalid node_id") |
| 300,000-byte string | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 200 `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` | 400 `INVALID_REQUEST` ("invalid node_id") |

- **Byte-for-byte comparison of successful register response**:
  - v1.1.0: `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}\n`
  - V2-1c not-served: `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}\n`
  - Result: **Identical byte for byte**.
- **Byte-for-byte comparison of `GET /v1/workers` response (not served)**:
  - Worker JSON fields in v1.1.0: `id`, `version`, `status`, `total_slots`, `available_slots`, `supported_tasks`, `last_heartbeat_at`, `registered_at`, `executes_concurrently`.
  - Worker JSON fields in V2-1c not-served: exactly the same 9 fields. `node_id` is completely omitted (key absent), identical to v1.1.0.

---

### Table 3: Leak Scan (ADR 4.4 Sensitive Fields, Point 3)
Evaluated across all response bodies and slog logs with a registered node containing full hardware and telemetry:

| Endpoint / Target | Sensitive Hardware Fields Present? | Telemetry Values Present? | `session_id` Present? | `seq` Present? | Echoes Stored Values? |
|---|---|---|---|---|---|
| `GET /v1/nodes` | None | None | No | No | No |
| `GET /v1/nodes/{id}` | Yes (permitted) | Yes (permitted) | **No** | **No** | Full view |
| `GET /v1/workers` (served) | None | None | No | No | No |
| `GET /v1/workers` (not served) | None | None | No | No | No |
| `POST /v1/workers/register` | None | None | No | No | No |
| `GET /v1/jobs/{id}` | None | None | No | No | No |
| `GET /v1/jobs/{id}/attempts` | None | None | No | No | No |
| 404 Error Body | None | None | No | No | No |
| 409 Error Body | None | None | No | No | No |
| 429 Error Body | None | None | No | No | No |
| slog: node register | None | None | No | No | No |
| slog: telemetry | None | None | No | No | No |
| slog: 429 rate limited | None | None | No | No | No |

Sensitive fields (`cpu_model`, `gpu_model`, `cpu_logical_cores`, `cpu_physical_cores`, `gpu_count`, `memory_total_bytes`, `disk_total_bytes`, `gpu_vram_total_bytes`, `agent_version`, `cpu_load_1m_milli`, `memory_available_bytes`, `disk_free_bytes`) appear **only** in `GET /v1/nodes/{id}`. `session_id` and `seq` appear in **no** response. Error bodies for 409 and 429 are completely opaque.

---

### Table 4: Node Ceiling and Paging (Point 5)

#### 1. Concurrency Overshoot (N=50 concurrent goroutines, MaxCount=10)
- In-Memory Repository: Succeeded = 11, Total in repo = 11, Overshoot = 1 (well within <= 50 bound).
- PostgreSQL Repository: Base = 0, MaxCount = 10, Succeeded = 39, New Total = 39, Overshoot = 29 (within <= 50 bound).
- Confirms documented property: soft ceiling overshoots by at most the number of concurrent requests.

#### 2. Paging and Limit Validation (`GET /v1/nodes`)

| Test Input / Case | HTTP Status | Response Summary |
|---|---|---|
| `limit=5` (exact full page) | 200 OK | `next_cursor` populated with 5th node ID; 5 nodes returned |
| `after=<5th ID>&limit=5` | 200 OK | `next_cursor: null`, `nodes: []` (empty list) |
| `limit=0` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=-1` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=501` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=1e2` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=1 ` / `limit= 1` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=+1` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=0x10` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `limit=99999999999999999999999` | 400 | `INVALID_REQUEST` ("limit must be between 1 and 500") |
| `after=3A00...` (uppercase hex) | 400 | `INVALID_REQUEST` ("after must be a canonical node id") |
| `after=3000%00...` (NUL byte) | 400 | `INVALID_REQUEST` ("after must be a canonical node id") |
| `after=` (empty value) | 400 | `INVALID_REQUEST` ("after must be a canonical node id") |
| `after=00000000-...` (nonexistent low) | 200 OK | Returns all nodes where id > 00000000... |
| `after=ffffffff-...` (nonexistent high) | 200 OK | Returns empty list `{"next_cursor":null,"nodes":[]}` |

---

## 4. Mutants Table (Point 8)

Executed in throwaway worktree `~/mc-mut-gemini-v21c` (since removed). Every mutant failed a named test and the test passed after clean restore.

| # | Mutation | Failing Named Test | Failure Output | After Restore |
|---|---|---|---|---|
| (a) | Node routes registered unconditionally (`if true`) | `TestAPI_NodeRoutes_NotServedWhenNotLocalOnly` | `nodes_test.go:515: POST /v1/nodes/register: 429 ... want the router's plain 404` | PASS |
| (b) | Workers list always includes `node_id` (`if true`) | `TestAPI_NodeRoutes_NotServedWhenNotLocalOnly` | `nodes_test.go:524: /v1/workers shows node_id when not local-only` | PASS |
| (c) | `node_id` wrong type fails register when not served | `TestAPI_RegisterWorker_NodeIDIgnoredWhenNotLocalOnly` | `nodes_test.go:538: case 5 node_id=5: 400 ... want 200 ...` | PASS |
| (d) | Paging limit ignored in `ListNodes` | `TestAPI_ListNodes_Paging` | `nodes_test.go:610: ?limit=2 returned 5 nodes, want at most limit 2` | PASS |
| (e) | Cursor `>=` instead of `>` in `ListNodes` | `TestAPI_ListNodes_Paging` | `nodes_test.go:624: paged order = ...` (duplicated cursor entries) | PASS |
| (f) | `nodeSummary` adds `cpu_model` | `TestAPI_ListNodes_SummaryOmitsSensitiveFields` | `nodes_test.go:363: summary keys = ... cpu_model ..., want ...` | PASS |
| (g) | Ceiling check removed from `Coordinator.RegisterNode` | `TestAPI_RegisterNode_NodeLimitReached` | `nodes_test.go:656: third node: 200 ... want 429 NODE_LIMIT_REACHED` | PASS |
| (h) | Telemetry `created_at` check removed | `TestAPI_Node_SampleAgeBeforeCreatedIsRejected` | `nodes_test.go:320: age older than the node: 200 {"status":"ACK"}, want 400` | PASS |
| (i) | `ErrNodeNotFound` on worker route mapped to 500 | `TestAPI_RegisterWorker_NodeIDOptional` | `nodes_test.go:238: status = 500 ... want 400` | PASS |
| (j) | `IsLocalOnlyAddr` returns true for empty host | `TestConfig_NodeLocalOnly_Table` | `node_config_test.go:31: IsLocalOnlyAddr(":8080") = true, want false` | PASS |

---

## 5. Quality Gate Counts (Point 10)

Executed in `~/MiniCloud-gemini` on database `minicloud_gemini_test` after `go clean -testcache`:

| Gate | Command | Result | Details / Output |
|---|---|---|---|
| `gofmt` | `gofmt -l .` | Empty | 0 lines |
| `go vet` | `go vet ./...` | Exit 0 | 0 warnings/errors |
| `go build` | `go build ./...` | Exit 0 | Clean build |
| `test-docs` | `make test-docs` | Exit 0 | `ok github.com/BlaZe08-Dev/Minicloud/tests/docs 1.913s` |
| `go test` (unset URL) | `env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...` | PASS 547 SKIP 133 FAIL 1 | Initial run encountered known flake `TestLocalCore_MultiProcessIntegration` (worker distribution race). Flake rerun alone 5 times: **5/5 PASS** (1.01s, 1.01s, 1.01s, 1.02s, 1.01s). |
| `make test-race` (unset URL) | `env -u MINICLOUD_TEST_DATABASE_URL make test-race` | PASS 548 SKIP 133 FAIL 0 | 0 data races, exit 0 |
| `make test-db` | `make test-db` | PASS 208 SKIP 0 FAIL 0 | 0 data races, exit 0 |
| `make test-security` | `make test-security` | PASS 80 SKIP 0 FAIL 0 | Exit 0 (58.29s) |

---

## 6. Builder Report Accuracy (Point 9)

- **SKIP rose by 3 claim**: Confirmed (**executed**). Three PostgreSQL tests (`TestConformance_Node_ListEmpty`, `TestConformance_Node_Paging`, `TestPostgresRepository_Node_TelemetryTakesKeyShareOnNode`) skip when `MINICLOUD_TEST_DATABASE_URL` is unset, raising unset SKIP from baseline 130 to 133.
- **N9, N13–N15 mutation claim**: Confirmed (**confirmed by code reading**). Builder noted in section 6 line 71 that N9 and N13–N15 were mutated against in-memory repository only.
- **Empty diffs claim**: Confirmed (**executed**). `git diff origin/main --stat -- internal/storage/postgres/migrations/ go.mod go.sum` printed nothing.
- **Beyond the ADR items 1–10**: Accurately reported by builder, except item 5 which overlooked the major performance degradation and clock skew failure mode identified in Finding F-01.
- **Gaps 1–5**: Gap 1 acknowledges clock skew, but minimizes it as an "approximation" rather than recognizing that clock skew leads to 100% false rejections of valid reports, and omits the 7-query / 2-transaction cost per telemetry report.

---

## 7. Not Verified

- `make test-outage` (reserved for owner).
- Telemetry throughput and memory profile above 10,000 workers on live PostgreSQL.
- Real cross-host network latency for node endpoints (local-only by design until V3-2).
- Behavior under container host-networking and reverse proxies (documented as honest limits in ADR 4.4 and `docs/protocol.md`).

---

## 8. Cleanup Confirmation

- Throwaway worktree `~/mc-mut-gemini-v21c-v110` removed (`git worktree remove`).
- Throwaway worktree `~/mc-mut-gemini-v21c` removed (`git worktree remove`).
- Temporary probe files in `~/gates-v21c-audit/` and `internal/` deleted.
- Working tree in `~/MiniCloud-gemini` is clean except for this report.
