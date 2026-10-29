Task: P4-4e fix round 3 (MALFORMED_JSON is not a fallback trigger)
Branch: agent/claude/P4-4e
Work tip (last commit before this report): 42369bb
Date: 2026-10-29

P4-4e fix round 3 is done and pushed on agent/claude/P4-4e.

A: MALFORMED_JSON removed from isValidationRejection; the fallback now triggers only on a 400 INVALID_REQUEST or OUTPUT_TOO_LARGE. The worker encodes its report with json.Marshal, so a MALFORMED_JSON answer means transport corruption; the result is dropped, the attempt reclaimed and the task re-runs (at-least-once), exactly as before round 2. Red first: TestWorker_ResultRejected400_MalformedJSON_NoFallback failed on the old code with 2 requests (a fallback was sent); now exactly 1 request and no fallback. Mutation M25 (put MALFORMED_JSON back) fails the new test; restored it passes. The existing fallback test no longer uses MALFORMED_JSON as a trigger (two codes: INVALID_REQUEST, OUTPUT_TOO_LARGE).
B: handoff fallback description corrected, Informational finding F-4e-28 added, M25 added; mutation count now 25 executed, 25 killed, 0 survived.

Gates: gofmt, vet, build, test-docs rc 0. Unset URL: PASS 248 SKIP 48 FAIL 0 (was 247/48/0: +1 PASS, the new test; SKIP unchanged). make test-db: PASS 81 SKIP 0 FAIL 0 (unchanged). make test-security twice: PASS 53 SKIP 1 FAIL 0 both runs, 54 tests, list unchanged and guard passed (the new test is in the worker package). -race -count=10 on the new test: ok.

Not verified: the fallback against a real coordinator rule other than through a corrupting proxy; default 30 s lease timing of the old loop (inferred); PostgreSQL production mutations beyond M18 and M20 not re-run against make test-db; cmd/mc untouched.
