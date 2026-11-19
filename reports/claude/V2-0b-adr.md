Task: V2-0b Stage A (ADR-0020, docs only)
Branch: agent/claude/V2-0b
Work tip (last commit before this report): 647568d
Date: 2026-11-19

## Final message

V2-0b Stage A is done: ADR-0020 is written with status Proposed (docs/adr/0020-round-robin-real-slots.md), and I stopped before Stage B. Commits: ae7cd3a (housekeeping), 647568d (ADR). Gates: test-docs rc 0, gofmt clean, vet and build rc 0, and the diff against main contains .md files only.

- **Decisions:** a worker can get a reservation while active attempts + live reservations < S, where S = total_slots if the worker declares concurrency and 1 otherwise, with at most 8 reservations per worker. Each decision recounts, so reservations made earlier in the same pass count. Rotation is unchanged. A fallback claim must also pass A+R<S (INV-10r). The worker loop needs no change: it polls again straight after each claim.
- **Item 6: I recommend option A.** Add an optional register field `executes_concurrently` and migration 000006; V2-1 then moves to 000007. Option B does not work because the v1.0.0 and main workers both send version "v0.1.0".
- **Item 5 bound:** MaxReservationsPerWorker = 8. The reason: 3 s maximum backoff + 8 × 200 ms = 4.6 s, which fits inside the 5 s accept window.
- Existing INV-10 tests keep their names (historical records cite them). They become the guard that an undeclared worker keeps one slot.
- Questions: Q1 (A, B or C), Q3 (keep 8 as a constant?) and Q5 (the task says SEC-016, but the total_slots bound is SEC-015) are in ADR-0020 Section 4, with five more.
