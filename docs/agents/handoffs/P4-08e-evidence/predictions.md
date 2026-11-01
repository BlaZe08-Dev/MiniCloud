# P4-08e gate predictions (written before any gate ran)

Baselines on main at 4b45f0d (given by the task): unset 349/84/0, security 54/0/0, db 154/0/0.

New top-level tests:
- internal/worker: `TestPollTiming_AcceptWindowExceedsBackoff`, `TestPollTiming_RunSleepsWithinMaxBackoff` (2).
- internal/storage (memory, shared cases): `TestConformance_ClaimPolicy_PerArm`, `TestConformance_JobView_AssignmentFields` (2).
- internal/storage/postgres (skip when the URL is unset): the same two names (2).
- internal/api: `TestAPI_Poll_PolicyFieldOnWire`, `TestAPI_Job_AssignmentFieldsAreAdditive` (2).
No test removed or renamed. No `TestFault_` or `TestAbuse_` test added.

| Gate | Prediction |
|---|---|
| unset suite | PASS 349 + 2 + 2 + 2 = 355, SKIP 84 + 2 = 86, FAIL 0 |
| make test-security | 54/0/0 (no security test added or changed) |
| make test-db (postgres + coordinator packages + grace integration test), three runs | 154 + 2 = 156/0/0 each run |
| race run, URL set: `./internal/coordinator ./internal/storage/... ./cmd/... ./internal/worker/...` | 276/0/0 (66 + 145 + 27 + 38 top-level tests by `go test -list`), assuming no test in these packages skips with the URL set |

If the final test list differs from this plan, the report gives the difference test by test.
