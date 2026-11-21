Task: V2-1a-AUDIT: read-only audit of slice V2-1a (branch agent/claude/V2-1a, tip 9508293)
Branch: agent/gemini/V2-1a-audit
Work tip (last commit before this report): 9508293
Date: 2026-11-21

# Audit Report: Slice V2-1a (Node Capability Contract & Platform Probe)

Audit of branch `agent/claude/V2-1a` at commit `9508293` (`docs(report): V2-1a report`), verified from branch `agent/gemini/V2-1a-audit`.
All findings, metrics, and gate executions reported below were executed and observed directly in this audit session. No results were simulated or assumed.

---

## 1. Verification of `internal/domain/node.go` against ADR-0022 Section 9

File `internal/domain/node.go` was verified line-by-line against the specification in ADR-0022 Section 9:

- **Limits, Charsets, and Ranges**:
  - `NodeIDLength = 36` (`node.go:45`): canonical lowercase RFC 9562 v4 UUID validated by `ValidateNodeID` (`node.go:137-163`) enforcing 36 bytes, hyphens at indices 8, 13, 18, 23, version nibble `'4'` at index 14, variant nibbles `'8'`, `'9'`, `'a'`, `'b'` at index 19, and lowercase hex digits elsewhere.
  - `display_name` (`node.go:47`, `175-186`): bounded by `MaxDisplayNameBytes = 64`, valid UTF-8, and rejects any control character (`strings.IndexFunc(s, unicode.IsControl) >= 0`), allowing empty string.
  - `contract_version` (`node.go:20-39`, `199-205`): `ContractVersion = 1`, `supportedContractVersions = []int{1}`, with helper `IsSupportedContractVersion` failing closed on any version outside `{1}`.
  - `agent_version` (`node.go:49`, `189-197`): bounded by `MaxAgentVersionBytes = 64`, rejects NUL (`strings.IndexByte(s, 0) >= 0`).
  - `os`, `arch` (`node.go:51`, `208-219`): bounded by `MaxOSArchBytes = 32`, regex equivalent `^[a-z0-9_]{1,32}$`.
  - `cpu_model`, `gpu_model` (`node.go:53`, `222-235`): bounded by `MaxModelBytes = 128`, printable ASCII (`0x20..0x7e`), rejects empty string when non-nil (`len(*s) < 1`).
  - `cpu_logical_cores`, `cpu_physical_cores` (`node.go:55`, `263-268`): bounded by `1..MaxNodeCount` (`MaxNodeCount = 1<<31 - 1`). Zero is rejected for `cpu_*` cores when known.
  - `gpu_count` (`node.go:55`, `269-271`): bounded by `0..MaxNodeCount`. Measured 0 is accepted (`gpu_count: 0` is allowed by domain contract).
  - `Logical >= Physical` (`node.go:272-274`): enforced when both cores are non-nil; returns `ErrInvalidRequest` if `*h.CPULogicalCores < *h.CPUPhysicalCores`.
  - Byte fields (`memory_total_bytes`, `disk_total_bytes`, `gpu_vram_total_bytes`, `memory_available_bytes`, `disk_free_bytes`): bounded by `0..MaxNodeBytes` (`MaxNodeBytes = 1<<53 - 1`, `node.go:57`, `275-281`, `289-292`).
  - `cpu_load_1m_milli` (`node.go:59`, `286-288`): bounded by `0..MaxLoadMilli` (`MaxLoadMilli = 1<<31 - 1`).
  - `sample_age_ms` (`node.go:61`, `324-326`): bounded by `0..MaxSampleAgeMS` (`MaxSampleAgeMS = 1<<31 - 1`).
  - `session_id` (`node.go:166-171`): reuses `ValidateNodeID` rules (lowercase v4 UUID).
  - `seq` (`node.go:63-64`, `321-323`): bounded by `MinTelemetrySeq = 1` and `MaxTelemetrySeq = 1<<53 - 1`.
  - `MaxHardwareHistory = 10` (`node.go:67`): exported constant pinning the 10-version retention limit.
- **Nil vs Zero Semantics**:
  - All measurement fields in `NodeHardware` (`node.go:72-83`) and `NodeTelemetry` (`node.go:85-91`) are defined as pointers (`*int64`, `*string`).
  - When `nil`, validation skips range checks and passes (`node.go:223`, `239`).
  - JSON serialization and deserialization preserve `null` vs `0` as tested in `internal/domain/node_test.go:249-303` (`TestNodeHardware_NilAndZeroStayDifferent`).
- **Deviations / Differences**:
  - None. All limits, types, and constraints match ADR-0022 Section 9 exactly.

---

## 2. Verification of `internal/platform/nodeid.go` and `nodeid_unix.go`

Verified against ADR-0022 Section 5, Task C1, and Task C2:

- **Atomic Publication**:
  - Publication is performed strictly via `ops.link(tmp, final)` (`nodeid.go:249`). `os.Rename` is never used, and the destination file is never written directly.
  - If `ops.link` returns `fs.ErrExist`, the process calls `readExistingNodeID(final)` (`nodeid.go:252`), reusing the existing file without overwriting.
  - If hard links are unsupported, it fails closed with a clear error (`nodeid.go:258`), with no fallback to rename or direct write.
- **Temp File Removal on All Failure Paths**:
  - In `createNodeID` (`nodeid.go:227-231`), a deferred cleanup function unconditionally removes `tmp` on all execution paths, including write failure, sync failure, close failure, chmod failure, link failure, and link collision.
- **Behavior if `syncDir` Fails After Publication**:
  - In `nodeid.go:261-264`, `ops.syncDir(dir)` is invoked after `ops.link`. If `syncDir` returns an error, `createNodeID` returns `"", fmt.Errorf(...)`.
  - At this point, the hard link `final` already exists in the directory.
  - On a subsequent call to `LoadOrCreateNodeID`, `readExistingNodeID` reads and validates the published file and succeeds.
  - *Assessment*: This behavior is safe, fail-closed, and acceptable. It ensures an I/O error on directory metadata sync is surfaced, while avoiding regenerating a different ID on the next startup.
- **Stale Temp Files Left by a Crash**:
  - If a process terminates abruptly (e.g., `SIGKILL`, kernel panic, power failure) after creating `tmp` (`nodeid.go:221`) but before the deferred `os.Remove` executes, the file `.node_id.tmp-<hex>` remains in the state directory.
  - `LoadOrCreateNodeID` has no cleanup routine for orphaned temp files from prior crashes.
  - *Assessment*: Stale temp files do not block future startups (since each attempt generates a unique random 8-byte hex suffix `nodeid.go:215-219` and reads only `node_id`). However, the builder's report did not document that crash-orphaned temp files are never cleaned up (Finding AUD-02).
- **TOCTOU Window Between `Lstat` and `Open`**:
  - In `readExistingNodeID` (`nodeid.go:172-191`), the file is inspected with `os.Lstat` at line 172 and subsequently opened with `os.Open` at line 191 without `O_NOFOLLOW`.
  - *Assessment*: Documented by the builder in Section 9 ("Not verified"). Since the containing directory is verified to be mode 0700 and owned by the current user (`nodeid_unix.go:16-32`), unprivileged concurrent local users cannot modify entries inside the directory. Acceptable within the local single-user threat model.
- **Parent Directory Symlinks**:
  - `prepareStateDir` (`nodeid.go:123`) and `inspectExistingDir` (`nodeid.go:160`) check `info.Mode()&fs.ModeSymlink != 0` on `dir` itself via `os.Lstat`.
  - It does not traverse upward to verify whether parent directories (e.g. `/home/user`) are symlinks.
  - *Assessment*: Acceptable and standard across Unix systems; enforcing that no parent component is a symlink would break standard OS setups where `/home` is a symlink to `/var/home` (e.g., Silverblue/Bazzite).
- **Existing Directory with Other Files**:
  - `prepareStateDir` checks only that `dir` is a directory, not a symlink, mode `& 0077 == 0`, and owned by `os.Geteuid()`. It does not require `dir` to be empty.
  - *Assessment*: Acceptable and expected, as `~/.minicloud` is designed to hold other node files (e.g. tokens, node agent state) in future slices.
- **Trailing Newline Variations and CRLF**:
  - `readExistingNodeID` (`nodeid.go:188-200`) restricts file size to `maxNodeIDFileBytes = 37` (36 UUID + 1 newline).
  - Trailing `\n` is stripped via `strings.TrimSuffix(string(b), "\n")`.
  - Exactly 36 bytes (no newline) is accepted.
  - Exactly 37 bytes (UUID + `\n`) is accepted.
  - CRLF (`\r\n`) yields 38 bytes, which exceeds 37 bytes and is rejected with an invalid size error (`nodeid_test.go:189`).
  - *Assessment*: Strictly conforms to Unix line endings and task C1. PROGRAMMATIC ID creation always emits LF.
- **Directory 0700 with `node_id` 0600 Owned by Another User**:
  - `checkDirAccess` verifies directory ownership via `checkOwner(dir, info, os.Geteuid())`.
  - `checkFileAccess` (`nodeid_unix.go:36-40`) validates `perm&0o077 != 0`, but does not call `checkOwner` on the file itself.
  - When `node_id` is mode 0600 owned by another user, `os.Open(final)` (`nodeid.go:191`) fails with `os.ErrPermission` (Permission denied).
  - `readExistingNodeID` returns `cannot read: permission denied`, and `LoadOrCreateNodeID` fails closed and halts startup.
  - *Assessment*: Safe fail-closed behavior under normal user privileges. An explicit ownership check in `checkFileAccess` would make this diagnostic cleaner (Finding AUD-03).

---

## 3. Verification of `internal/platform/probe_linux.go` against ADR-0022 Section 6.3

- **Physical Cores Calculation**:
  - `parseCPUInfo` (`probe_linux.go:165-216`) tracks distinct `physical id` and `core id` pairs using a map `pairs[phys+"/"+core] = struct{}{}`.
  - Only processor blocks containing both keys populate `pairs`.
  - If keys are missing (e.g., ARM kernels), `ci.physical` remains `nil` (never 0), as verified by `TestProbeLinux_ParsesFixtures/nocorekeys` (`probe_linux_test.go:48`).
- **cgroup v2 `memory.max` Lowering and `memory.current` Headroom**:
  - `Hardware()` (`probe_linux.go:63-66`) reads `cgroupMemoryMax()` and lowers `MemoryTotalBytes` if `limit < *h.MemoryTotalBytes`. If `memory.max` is `"max"` or unreadable, `MemoryTotalBytes` remains host total.
  - `Telemetry()` (`probe_linux.go:86-90`) computes headroom as `max(0, memory.max - memory.current)` via `cgroupMemoryHeadroom()` and lowers `MemoryAvailableBytes` if headroom is smaller.
  - *Nested cgroups*: The probe reads specifically `/sys/fs/cgroup/memory.max` and `/sys/fs/cgroup/memory.current`. Inside containers with their own cgroup namespaces, this correctly reflects the container bounds. On the host within a systemd service slice, the limit resides under `/sys/fs/cgroup/<slice>/memory.max`, which is not read. The ADR does not state the cgroup path, so threat T8 is partly covered as correctly identified by the builder (Gap G3, Finding AUD-05).
- **`MemAvailable` Missing Gives Nil**:
  - If `MemAvailable` is absent from `/proc/meminfo`, `meminfoBytes` returns `nil`.
  - `Telemetry()` checks `if t.MemoryAvailableBytes != nil` before checking cgroup headroom; thus, missing `MemAvailable` remains `nil` (never 0, never replaced by cgroup limits).
- **Load Average Conversion and Rounding**:
  - `parseLoadAvgMilli` (`probe_linux.go:269-284`) parses the 1-minute load float, applies `math.Round(f * 1000)`, bounds to `MaxLoadMilli`, and returns `*int64`. NaN, Inf, and negative numbers return `nil`.
- **`Statfs` Units**:
  - `disk()` (`probe_linux.go:129-139`) uses `st.Bsize` with `st.Blocks` for total bytes and `st.Bavail` (unprivileged free blocks) for free bytes, avoiding `st.Bfree` (which includes root-reserved blocks). Overflow is guarded by `capBytes`.
- **`cpu_model` Sanitization**:
  - `sanitizeModel` (`probe_linux.go:221-236`) replaces any byte `< 0x20` or `> 0x7e` with `'?'`, trims whitespace, truncates to `MaxModelBytes` (128), and returns `nil` if empty.
- **GPU Fields Always Nil**:
  - `linuxProbe.Hardware()` does not assign GPU fields; `GPUCount`, `GPUModel`, and `GPUVRAMTotalBytes` remain `nil` (never 0). Tested across all 7 fixtures including fake sysfs entries in `TestProbeLinux_GPUFieldsAlwaysNil`.
- **Isolation from Real Host `/proc` and `/sys`**:
  - In `linuxProbe`, all file reads go through `p.readFile(rel)` which joins with `p.root` (`probe_linux.go:35-45`). No production code path accesses `/proc` or `/sys` directly when a test specifies a fixture root.

---

## 4. Test Suite Audit against ADR-0022 Section 14.1

Every test named in ADR-0022 Section 14.1 was verified for presence and assertion rigor:

1. `TestNodeID_Validate_Table` (`internal/domain/node_test.go:16`): Covers valid v4, variants 8/9/b, uppercase, braces, versions 1/5, variants c/7, 35/37 bytes, empty, non-hex, moved hyphens, newlines, NUL bytes. Asserts `ErrInvalidRequest`.
2. `TestNodeHardware_Validate_Table` (`internal/domain/node_test.go:73`): Tests all Section 9 caps at edge and edge+1, logical < physical failure, nil handling, negative numbers.
3. `TestNodeTelemetry_Validate_Table` (`internal/domain/node_test.go:137`): Tests load avg, mem available, disk free, sample age, and seq at boundary and boundary+1.
4. `TestNodeIDFile_CreateThenReuse` (`internal/platform/nodeid_test.go:60`): Asserts 0700 dir, 0600 file, exact content (`UUID\n`), and identical UUID on second start.
5. `TestNodeIDFile_ConcurrentFirstStart_SameID` (`internal/platform/nodeid_test.go:104`): 16 goroutines race on empty dir; asserts 0 errors, all 16 return identical UUID, exactly one file exists.
6. `TestNodeIDFile_CorruptFile_Refuses` (`internal/platform/nodeid_test.go:180`): 11 corrupt variants tested; asserts refusal, empty returned ID, error naming path, file untouched.
7. `TestNodeIDFile_WideMode_Refuses` (`internal/platform/nodeid_perm_unix_test.go:51`): Tests 8 wide modes (0644, 0640, etc.); asserts refusal, file mode and content preserved.
8. `TestNodeIDFile_DirectoryUnwritable_Refuses` (`internal/platform/nodeid_test.go:324`): Tests non-creatable parent, unwritable 0500 dir, failed write, failed fsync, failed entropy; asserts no files created.
9. `TestProbeLinux_ParsesFixtures` (`internal/platform/probe_linux_test.go:36`): Tests `full`, `nocorekeys`, `nomemavailable` fixtures; asserts nil for missing keys.
10. `TestProbeLinux_CgroupLowersMemory` (`internal/platform/probe_linux_test.go:139`): Tests memory.max lowering total, headroom lowering available, "max" leaving total unchanged, and headroom never raising host available.
11. `TestProbeLinux_GPUFieldsAlwaysNil` (`internal/platform/probe_linux_test.go:197`): Asserts nil GPU fields across all 7 fixtures including fake sysfs GPU device.
12. `TestProbeFallback_OnlyOSArch` (`internal/platform/probe_test.go:13`): Tests fallback probe fills only runtime OS and Arch, leaving all other fields nil.
13. `TestArchitecture_PlatformIsolation` (`tests/architecture/boundaries_test.go:145`): Verifies domain, coordinator, storage, api do not import platform.

- **Helper Process Concurrency**:
  - `TestNodeIDFile_HelperProcesses_RaceOneDirectory` (`nodeid_test.go:140-178`) uses `exec.Command(os.Args[0])` to spawn 6 real OS processes. Processes synchronize on a gate file via `TestMain` child mode (`nodeid_helper_test.go:18-46`), ensuring true OS-level process racing.
- **Unix Permissions File Exclusion on Windows**:
  - `nodeid_perm_unix_test.go:1` carries `//go:build unix`.
  - Windows does not satisfy `unix`, completely excluding this file on Windows.
  - `nodeid_perm_other_test.go:1` carries `//go:build !unix` and issues `t.Skip` with clear rationale.

---

## 5. Mutation Testing (Executed in Throwaway Worktree `~/mc-mut-gemini-v21a`)

A dedicated throwaway worktree was created at `~/mc-mut-gemini-v21a`, verified, mutated, restored, and subsequently removed.

| # | Mutant Description | Code Location | Failing Test | Mutation Result | Result After Restore |
|---|---|---|---|---|---|
| **N6** | Remove corrupt file and regenerate new ID | `internal/platform/nodeid.go:114` | `TestNodeIDFile_CorruptFile_Refuses` | **FAIL** (`want refusal, got id "cd2eded5-..."`) | **PASS** (`ok 0.021s`) |
| **N32** | Publish with `os.Rename` instead of `os.Link` | `internal/platform/nodeid.go:61` | `TestNodeIDFile_ExistingValidID_NeverReplacedWhenPublicationRaces` | **FAIL** (`returned new id, want existing id`) | **PASS** (`ok 0.008s`) |
| **N33** | Accept wide existing directory (bypass mode check) | `internal/platform/nodeid_unix.go:16` | `TestStateDir_Existing_PermissionRules` | **FAIL** (`want refusal, got UUID, <nil>`) | **PASS** (`ok 0.014s`) |
| **GPU** | Linux probe reports `gpu_count = 0` | `internal/platform/probe_linux.go:53` | `TestProbeLinux_GPUFieldsAlwaysNil` | **FAIL** (`gpu_count = 0, want nil (unknown)`) | **PASS** (`ok 0.009s`) |
| **SYM** | Use `os.Stat` instead of `os.Lstat` (follow symlink) | `internal/platform/nodeid.go:172` | `TestNodeIDFile_Symlink_Refuses` | **FAIL** (`got id, want symlink refusal`) | **PASS** (`ok 0.006s`) |

*Throwaway worktree status*: Removed via `git worktree remove /home/blazeee/mc-mut-gemini-v21a --force`. Confirmed non-existent.

---

## 6. Architecture Boundary Test Analysis

`TestArchitecture_PlatformIsolation` in `tests/architecture/boundaries_test.go`:

1. **Mutation Test**:
   - Added `import _ "github.com/BlaZe08-Dev/Minicloud/internal/platform"` to `internal/domain/node.go`.
   - Result: Test **FAILED** as expected:
     ```
     --- FAIL: TestArchitecture_PlatformIsolation (0.01s)
         boundaries_test.go:84: Architecture boundary violation! internal/domain, internal/coordinator, internal/storage and internal/api must not import internal/platform (ADR-0022 Section 6.2).
             Found violations:
             node.go: imports forbidden package github.com/BlaZe08-Dev/Minicloud/internal/platform
     ```
   - Restored and confirmed passing.
2. **Blank and Dot Imports**:
   - The test parses file imports using `go/parser.ParseDir` with `parser.ImportsOnly` and extracts `strings.Trim(imp.Path.Value, "\"")`.
   - Blank imports (`_ "..."`), dot imports (`. "..."`), and aliased imports (`p "..."`) retain their full import path in `imp.Path.Value`.
   - Therefore, blank and dot imports are properly detected and rejected.
3. **Test Files Coverage**:
   - In `checkPackageBoundaries` (`boundaries_test.go:68-70`):
     ```go
     if strings.HasSuffix(baseName, "_test.go") {
         continue
     }
     ```
   - The boundary test explicitly skips all `*_test.go` files.
   - Tested empirically: Adding `_ "github.com/BlaZe08-Dev/Minicloud/internal/platform"` to `internal/domain/node_test.go` **PASSED**.
   - *Finding*: `TestArchitecture_PlatformIsolation` does not protect test files from importing `internal/platform` (Finding AUD-01).

---

## 7. Report Accuracy and Gap Assessment

### Citation Sample Verification (17 citations sampled from `reports/claude/V2-1a.md`):

1. `internal/domain/node.go:20-23`: `ContractVersion = 1` and `supportedContractVersions = []int{ContractVersion}` (Verified exact match).
2. `internal/domain/node.go:26-37`: `SupportedContractVersions` and `IsSupportedContractVersion` (Verified exact match).
3. `internal/domain/node.go:43-68`: Section 9 constant limits (Verified exact match).
4. `internal/domain/node.go:72-107`: Contract struct types (Verified exact match).
5. `internal/domain/node.go:120`: `FormatNodeID` (Verified exact match).
6. `internal/domain/node.go:137`: `ValidateNodeID` (Verified exact match).
7. `internal/domain/node.go:249`: `(NodeHardware) Validate()` (Verified exact match).
8. `internal/domain/node.go:285`: `(NodeTelemetry) Validate()` (Verified exact match).
9. `internal/domain/node.go:296`: `(NodeRegistration) Validate()` (Verified exact match).
10. `internal/domain/node.go:314`: `(NodeTelemetryReport) Validate()` (Verified exact match).
11. `internal/platform/probe.go:16-19`: `Probe` interface definition (Verified exact match).
12. `internal/platform/probe_linux.go:63`: cgroup v2 memory.max lowering (Verified exact match).
13. `internal/platform/nodeid.go:35`: `DefaultStateDir` (Verified exact match).
14. `internal/platform/nodeid.go:95`: `LoadOrCreateNodeID` (Verified exact match).
15. `internal/platform/nodeid.go:171-172`: `readExistingNodeID` / `Lstat` check (Verified exact match).
16. `internal/platform/nodeid.go:249`: `ops.link(tmp, final)` publication (Verified exact match).
17. `tests/architecture/boundaries_test.go:145`: `TestArchitecture_PlatformIsolation` (Verified exact match).

### Review of "Beyond the ADR" and Gaps G1..G6:

- The builder's 14 "Beyond the ADR" entries and gaps G1..G6 accurately capture the implementation choices and specification ambiguities.
- Additional gaps identified by this audit:
  - Architecture isolation test skips test files (`*_test.go`) (Finding AUD-01).
  - Stale `.node_id.tmp-*` files left by abrupt crashes are not cleaned up (Finding AUD-02).
  - File ownership verification is omitted in `checkFileAccess` (Finding AUD-03).

---

## 8. Quality Gates Execution

Executed in worktree `~/MiniCloud-gemini`:

1. `gofmt -l .`: Clean (no output).
2. `go vet ./...`: Clean (exit code 0).
3. `go build ./...`: Clean (exit code 0).
4. `make test-docs`: Clean (`ok github.com/BlaZe08-Dev/Minicloud/tests/docs 2.307s`).
5. `env -u MINICLOUD_TEST_DATABASE_URL go test -v -count=1 ./...`:
   - **PASS: 502**
   - **SKIP: 101**
   - **FAIL: 0**
   - Verified: Matches the builder's reported counts exactly (+45 new tests over main's 457).
6. `env -u MINICLOUD_TEST_DATABASE_URL make test-race`:
   - Exit code: 0
   - `DATA RACE` count: **0**
7. Cross-Platform Vet and Test Compilation:
   - `GOOS=windows go vet ./internal/platform/...`: Clean (exit code 0).
   - `GOOS=darwin go vet ./internal/platform/...`: Clean (exit code 0).
   - `GOOS=windows go test -c -o /dev/null ./internal/platform/`: Clean (exit code 0).
   - `GOOS=darwin go test -c -o /dev/null ./internal/platform/`: Clean (exit code 0).
8. Database tests (`make test-db`, `make test-outage`): Not run, by instruction (persistence not touched in slice V2-1a).

---

## Findings Table

| ID | Severity | File:Line | What | Why it matters | Suggested Fix Wording |
|---|---|---|---|---|---|
| **AUD-01** | Minor | `tests/architecture/boundaries_test.go:68-70` | `checkPackageBoundaries` skips `_test.go` files when asserting package import boundaries. | A unit or integration test in `internal/domain`, `internal/coordinator`, `internal/storage`, or `internal/api` could import `internal/platform`, breaking layer decoupling in test suites without detection. | In `boundaries_test.go`, check all `.go` files including `_test.go` for `forbiddenPlatformImports` in `TestArchitecture_PlatformIsolation`. |
| **AUD-02** | Minor | `internal/platform/nodeid.go:219-231` | Temp files `.node_id.tmp-<hex>` left behind if the process is killed (`SIGKILL` / crash) prior to `defer os.Remove` execution are never cleaned up. | Orphaned hidden temp files accumulate if crashes occur during first start. Subsequent starts succeed, but disk clutter remains. | Add documentation in `nodeid.go` noting that crash-orphaned temp files are not swept, or implement a cleanup in `prepareStateDir` removing stale `tempPrefix` files. |
| **AUD-03** | Info | `internal/platform/nodeid_unix.go:36-40` | `checkFileAccess` validates mode bits `perm&0o077 != 0` but does not check file ownership against `os.Geteuid()`. | If `node_id` is mode 0600 owned by another user, unprivileged `os.Open` fails closed with permission denied. However, elevated or root callers would open the foreign file without an explicit ownership error. | Add `return checkOwner(path, info, os.Geteuid())` to `checkFileAccess` for symmetry with `checkDirAccess`. |
| **AUD-04** | Info | `internal/platform/nodeid.go:261-264` | If `ops.syncDir(dir)` fails after `ops.link(tmp, final)`, an error is returned but the hard link `final` remains published. | On the next call to `LoadOrCreateNodeID`, the published file is read and startup succeeds. This is safe, but the behavior should be explicitly commented. | Add a code comment in `createNodeID` documenting that returning an error on `syncDir` leaves the published file intact so that retry preserves identity stability. |
| **AUD-05** | Info | `internal/platform/probe_linux.go:100` | Cgroup v2 limits are read only from `/sys/fs/cgroup/memory.max`, not accounting for nested host cgroup paths from `/proc/self/cgroup`. | Threat T8 is only covered for containerized / cgroup-namespaced workers; host systemd service slices are not scoped. | Document in ADR-0022 and `probe_linux.go` that host systemd nested cgroups are an accepted limitation of V2-1a, to be addressed in a future slice. |

---

## Not Verified

- Hardware probes (`NewProbe("/")`) were not run against real host hardware or physical GPU hardware in this session; verification relied on Linux fixtures (`testdata/`).
- Actual test execution on native Windows and macOS operating systems (compiled and vetted via cross-compilation only).
- Hard link behavior on filesystems lacking hard link support (e.g., FAT32); verified using mock error injection.
- Direct test execution under root user credentials for foreign UID ownership rejection.
- Database tests (`make test-db`, `make test-outage`), omitted per task specification since V2-1a does not alter storage or database schema.
