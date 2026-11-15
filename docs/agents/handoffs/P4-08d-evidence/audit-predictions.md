# P4-08d audit: gate predictions (written before any audit gate ran)

Tip under audit: b5ac949. Review branch agent/claude/P4-08d-review.

| Gate | Prediction |
|---|---|
| gofmt / vet / build | clean / rc 0 / rc 0 |
| make test-docs | rc 0 |
| env -u MINICLOUD_TEST_DATABASE_URL go test -v -race -count=1 ./... | PASS 343 SKIP 84 FAIL 0, 0 DATA RACE |
| make test-security | 54/0/0 |
| make test-db | 149/0/0, 0 DATA RACE |
| race, URL set, ./internal/coordinator ./internal/storage/... ./cmd/... | 228/0/0, 0 DATA RACE (builder's figure) |
