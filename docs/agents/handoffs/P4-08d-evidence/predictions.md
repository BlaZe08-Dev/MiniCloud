# P4-08d gate predictions (written before any gate ran)

Baselines on main after P4-12 (given by the task): unset 324/81/0, security 54/0/0, db 134/0/0.

Planned new top-level tests:
- internal/coordinator (memory, run by the unset suite and by make test-db): 12.
- internal/storage (memory, shared cases): 2.
- internal/storage/postgres (skip when the URL is unset): 3.
- cmd/coordinator: 5.

| Gate | Prediction |
|---|---|
| unset suite | PASS 324 + 12 + 2 + 5 = 343, SKIP 81 + 3 = 84, FAIL 0 |
| make test-security | 54/0/0 (no security test added or changed) |
| make test-db (postgres + coordinator packages + grace integration test) | 134 + 12 + 3 = 149/0/0 |

If the final test list differs from this plan, the report gives the difference test by test.
