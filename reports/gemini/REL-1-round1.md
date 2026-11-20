Task: REL-1 Fix Round 1
Branch: agent/gemini/REL-1
Work tip (last commit before this report): 5b3dab3
Date: 2026-11-20

## REL-1 Fix Round 1 Summary
- `docs/release/v1.1.0-notes.md`: Rewrote Rollback section in plain user-facing language; removed references to "Stage B", "question Q4", "audit evidence mixed-rollback/summary.txt", and "OLD"; preserved all 3 rollback options and both operator steps (`MINICLOUD_WORKER_SLOTS=1` and `UPDATE workers SET executes_concurrently = false;` or worker restart).
- `docs/release/RELEASE_CHECKLIST.md`: In Step 1, added instructions to close browser and IDE first, updated gate commands with exact sequence (`gofmt -l .`, `go vet ./...`, `go build ./...`, `go clean -testcache`, `make test-docs`, `go test -v -count=1`, `make test-race`, `make test-db`, `make test-security`, `make test-outage`), and noted counting top-level `--- PASS`, `--- SKIP`, `--- FAIL` lines. In Step 5, added copying release notes to a temporary file outside the repo to append gate results for `--notes-file`. In Step 6, removed `AGENTS.md` and added updating the Project context (orchestrator).
- `make test-docs`: PASS (0 violations).
