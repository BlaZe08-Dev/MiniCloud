Task: P4-08b fix round 2 (audit of tip 46ca30a, verdict CHANGES REQUIRED)
Branch: agent/claude/P4-08b
Work tip (last commit before this report): 20e6c18
Date: 2026-10-31

I read the audit on `origin/agent/claude/P4-08b-review` (`docs/agents/reviews/P4-08b.md` and the `audit_probe_*.go.txt` files on that branch) without merging, cherry-picking or copying anything from it. I rewrote the probes I adopted as real tests in `internal/`. Evidence label: **executed** for every result below, unless a line says otherwise.

## Status per audit finding

| Finding | Status | Where |
|---|---|---|
| F-5a-1 (major) zero-value settings under round_robin | **Fixed** (3cf0f43). `NewService` starts from `DefaultAssignmentSettings()`. `SetAssignmentSettings` validates with the Section 5.5 ranges and the A1 cross-checks, returns `domain.ErrInvalidRequest`, and keeps the previous settings. `SetDispatchPolicy("round_robin")` refuses invalid settings. Red first: `TestSetDispatchPolicy_RoundRobinWithoutSettings_UsesDefaults` failed on the unfixed tree with `policy="round_robin_fallback" reason="lapses"` (`fix2-red-F5a1.log`). Mutations M24 (default), M25 (SetAssignmentSettings validation), M26 (SetDispatchPolicy check), M27 and M28 (Validate rules): all KILLED. | `internal/coordinator/assignment.go`, `service.go` |
| F-5a-2 capacity check at claim | **Test added**: `TestConformance_Claim_RespectsWorkerCapacity`; M31 (audit A11) KILLED | `internal/storage/inmemory_assignment_test.go` |
| F-5a-3 lapse-limit exclusion and L-1 | **Tests added**: `TestConformance_AssignPass_ExcludesJobAtLapseLimit` (M32, A05) and `TestConformance_ClaimLapseArm_ClosedBelowLimit` (M33, A07), both KILLED | same |
| F-5a-4 no lapse on unhealthy revoke | **Test added**: `TestConformance_AssignPass_UnhealthyRevokeCountsNoLapse`; M34 (A09) KILLED | same |
| F-5a-5 claim at exact expiry | **Test added**: `TestConformance_ClaimOwnArm_ClosedAtExactExpiry`; M35 (A08) KILLED | same |
| F-5a-6 retry not_before in the age arm | **Test added**: `TestConformance_ClaimAgeArm_UsesNotBefore`; M36 (A10) KILLED | same |
| F-5a-7 worker ID order in the assignment event | **Test added**: `TestConformance_AssignEvent_CandidatesInIDOrder`; M37 (A04) KILLED | same |
| F-5a-8 malformed values default silently | **Fixed** for the new variables (157a12d). The six `MINICLOUD_ASSIGNMENT_*` variables parse strictly, and a set value that does not parse (an empty value included) fails startup with an error that names the variable. `MINICLOUD_DISPATCH_POLICY` was already strict (any other value fails `validateAssignment`); the new test covers it. Test: `TestLoadCoordinatorConfig_AssignmentMalformedValuesRejected` (14 subtests). Red first: 12 subtests failed and the 2 policy subtests passed (`fix2-red-F5a8.log`). M29 KILLED. The existing variables were not changed (see Open items). | `internal/config/config.go` |
| F-5a-9 memory pass-lock skip | **Test added**: `TestConformance_AssignPass_SkipsWhenPassLockHeld` (seam `HoldPassLockForTest` in `internal/storage/inmemory_export_test.go`). It checks Skipped, no reservation and an unchanged heartbeat. A 2 s deadline makes a pass that blocks fail instead of hang. M38 (A01) KILLED | same |
| F-5a-10 A1 interval equal to the accept window | **Test added**: `TestLoadCoordinatorConfig_AssignmentIntervalEqualToWindowAccepted`; M30 (A14) KILLED. `TestAssignmentSettings_Validate` pins the same edge for the service (M27) | `internal/config/assignment_config_test.go` |
| F-5a-11 invalid policy name | **Test added**: `TestSetDispatchPolicy_RejectsInvalidPolicyName`; M39 (A17) KILLED | `internal/coordinator/assignment_test.go` |
| F-5a-12 refresh threshold, poll-write throttle | **Deferred to slice 5c**, as instructed. ADR implementation note 9 names the planned tests. The A12 and A13 mutations were not rerun | ADR-0017 |
| F-5a-13 clock mixing | **Recorded as a slice 5b requirement** (ADR implementation note 8): in PostgreSQL the pass heartbeat and every staleness comparison use the database clock (`now()`) for both the write and the read, and the Service never compares its own clock with a repository time. The in-memory code was not changed | ADR-0017 |
| F-5a-14, F-5a-16, F-5a-17 (nits) | **Not addressed**, because the fix round 2 instructions did not include them. Listed in the handoff | — |
| F-5a-15 (13 vs 14 rejection cases) | **Fixed** in the handoff (14) | `docs/agents/handoffs/P4-08b.md` |
| Mutation index: INV-19 outage "no own mutation" | **Fixed**. M40 applies M20's exact change and runs it against `TestPoll_AgeArm_AfterOutage_RefreshesFirst`: KILLED. The coverage table now names M20 and M40 for INV-19 and M38 for INV-22 (memory). Totals: 42 runs, 42 killed | `mutation-index.md` |

M24 note: removing the default in `NewService` is killed through the M26 check. With the zero settings, `SetDispatchPolicy("round_robin")` now fails before any poll, so the test fails at that call and not at the claim.

The mutations M24 to M40 ran in a throwaway worktree, `~/mc-mut-claude`, at c10787e, with `go test -race -count=1 -v -run '^<test>$'`. Each red log has the named test's `--- FAIL` and its assertion. Each green log has 1 PASS and 0 FAIL. The worktree was removed afterwards.

## Open items for the orchestrator

- The existing env helpers `getIntEnv` and `getDurationEnv` in `internal/config/config.go` still default silently on a malformed value, for every variable that is not an assignment variable (for example `MINICLOUD_WORKER_TIMEOUT` and `MINICLOUD_DB_MAX_CONNS`). This finding is for you to register.
- `AssignmentSettings.Validate` (coordinator) duplicates the rules of `validateAssignment` (config). Each has its own tests, but a rule change must edit both.

## Counts: predicted, then actual

Predicted before the run: unset 296 + 15 new top-level tests (5 coordinator, 2 config, 8 storage) = 311/58/0; security 54/0/0 (untouched); db 91 + 5 coordinator = 96/11/0. The actual counts below match.

## Gates (run once, after the last edit, on tree 20e6c18, clean)

```
## tree: 20e6c18, status: [0 changed]
$ gofmt -l .
[]
$ go vet ./...
EXIT=0
$ go build ./...
EXIT=0
$ make test-docs
go test ./tests/docs
ok  	github.com/BlaZe08-Dev/Minicloud/tests/docs	2.221s
EXIT=0
gate-unset-v: PASS 311 SKIP 58 FAIL 0 EXIT=0
gate-security: PASS 54 SKIP 0 FAIL 0 EXIT=0
gate-unset-v: PASS 311 SKIP 58 FAIL 0 EXIT=0   (env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./...)
gate-security: PASS 54 SKIP 0 FAIL 0 EXIT=0    (make test-security)
gate-db: PASS 96 SKIP 11 FAIL 0 EXIT=0         (make test-db, minicloud_claude_test)
```

## git diff --stat 46ca30a..20e6c18

```
 docs/adr/0017-assignment-round-robin.md            |   8 +
 .../handoffs/P4-08b-evidence/fix2-red-F5a1.log     |  11 ++
 .../handoffs/P4-08b-evidence/fix2-red-F5a8.log     |  47 +++++
 .../handoffs/P4-08b-evidence/mut-M24-green.log     |   9 +
 .../handoffs/P4-08b-evidence/mut-M24-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M24.diff  |  12 ++
 .../handoffs/P4-08b-evidence/mut-M25-green.log     |   9 +
 .../handoffs/P4-08b-evidence/mut-M25-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M25.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M26-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M26-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M26.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M27-green.log     |  44 +++++
 .../handoffs/P4-08b-evidence/mut-M27-red.log       |  46 +++++
 docs/agents/handoffs/P4-08b-evidence/mut-M27.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M28-green.log     |  44 +++++
 .../handoffs/P4-08b-evidence/mut-M28-red.log       |  46 +++++
 docs/agents/handoffs/P4-08b-evidence/mut-M28.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M29-green.log     |  34 ++++
 .../handoffs/P4-08b-evidence/mut-M29-red.log       |  47 +++++
 docs/agents/handoffs/P4-08b-evidence/mut-M29.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M30-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M30-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M30.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M31-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M31-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M31.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M32-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M32-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M32.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M33-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M33-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M33.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M34-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M34-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M34.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M35-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M35-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M35.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M36-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M36-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M36.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M37-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M37-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M37.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M38-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M38-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M38.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M39-green.log     |   6 +
 .../handoffs/P4-08b-evidence/mut-M39-red.log       |   8 +
 docs/agents/handoffs/P4-08b-evidence/mut-M39.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mut-M40-green.log     |  11 ++
 .../handoffs/P4-08b-evidence/mut-M40-red.log       |  11 ++
 docs/agents/handoffs/P4-08b-evidence/mut-M40.diff  |  13 ++
 .../handoffs/P4-08b-evidence/mutation-index.md     |  33 +++-
 docs/agents/handoffs/P4-08b.md                     |  30 ++-
 internal/config/assignment_config_test.go          |  47 +++++
 internal/config/config.go                          |  63 +++++-
 internal/coordinator/assignment.go                 |  58 +++++-
 internal/coordinator/assignment_test.go            | 137 ++++++++++++-
 internal/coordinator/export_assignment_test.go     |   9 +
 internal/coordinator/service.go                    |   4 +-
 internal/storage/inmemory_assignment_test.go       | 215 +++++++++++++++++++++
 internal/storage/inmemory_export_test.go           |   9 +
 64 files changed, 1345 insertions(+), 17 deletions(-)
```

Commits: 3cf0f43, 157a12d, c10787e, 596b337, 20e6c18. New tip: the commit that carries this report, pushed to `origin/agent/claude/P4-08b`.
