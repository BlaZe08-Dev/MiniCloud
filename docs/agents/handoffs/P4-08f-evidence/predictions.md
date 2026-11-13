# P4-08f gate predictions (written before any gate ran on the final tree)

Baselines from the task: unset 361 PASS / 87 SKIP / 0 FAIL, security 54 / 0 / 0, db 157 / 0 / 0. Counts are top-level `--- PASS`, `--- SKIP`, `--- FAIL` lines.

New top-level tests: 13 `TestFault_` in tests/faultinject and 5 `TestAbuse_` in tests/abuse. The property test and the worker tidy-ups change existing tests only (a subtest was added, no top-level test).

| Gate | Predicted | Why |
|---|---|---|
| gofmt, go vet, go build, make test-docs | clean | no production change; the ADR notes cite only names that exist |
| unset suite (`go test -race ./...`, URL unset) | 379 PASS / 87 SKIP / 0 FAIL | each new test runs its memory subtest and PASSES at top level; the PostgreSQL subtest is not created without a URL, so no new SKIP |
| make test-security | 72 PASS / 0 SKIP / 0 FAIL | 54 + 13 + 5; the list file has 72 names |
| make test-db (three runs) | 157 PASS / 0 SKIP / 0 FAIL each | the coordinator and PostgreSQL packages gain no top-level test; the strengthened property test is one existing test |
| race on coordinator, storage, cmd, worker, api | 0 FAIL, no DATA RACE | no baseline count was given; the count is reported as measured |
