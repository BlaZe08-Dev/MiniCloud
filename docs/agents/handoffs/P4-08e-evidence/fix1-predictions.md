# P4-08e fix round 1: gate predictions (written before any gate ran)

Baselines at 74df64d: unset 355/86/0, security 54/0/0, db 156/0/0, race 276/0/0 (`./internal/coordinator ./internal/storage/... ./cmd/... ./internal/worker/...`).

New top-level tests planned:
- `internal/storage`, `internal/storage/postgres`: `TestConformance_ClaimPolicy_BothArms` (shared case, F-R2). Memory PASS, PostgreSQL SKIP without a URL.
- `internal/api`: `TestAPI_SubmitJob_ResponseCarriesAssignmentFields` (F-R3).
- `internal/worker`: `TestPollTiming_RealClockSleeps` (F-R4, about 3.5 s).
F-R1 changes an existing test (the `relation` subtest of `TestPollTiming_AcceptWindowExceedsBackoff`) and adds no top-level test.

| Gate | Prediction |
|---|---|
| unset suite | PASS 355 + 1 + 1 + 1 = 358, SKIP 86 + 1 = 87, FAIL 0 |
| make test-security | 54/0/0 (no test with a fault or abuse prefix added) |
| make test-db, three runs | 156 + 1 = 157/0/0 each run |
| race, URL set, old set (coordinator, storage/..., cmd/..., worker/...) | 276 + 2 + 1 = 279/0/0 |
| race, URL set, with `./internal/api/...` | 279 + 28 (27 existing + 1 new) = 307/0/0 |

Mutants (each alone): audit-I4 and the F-R2, F-R3 and F-R4 mutants below are predicted red with the new tests and green after restore.
