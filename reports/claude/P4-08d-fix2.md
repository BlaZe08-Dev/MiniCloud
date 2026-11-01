# P4-08d fix round 2 (docs only)

Problem: ADR-0017 note 25 said the cap anchor lags the real start of the pending state by at most one pass interval, but its FS-19 formula and the 48 s example left the lag out.

Corrected to `L x (accept window + pass interval) + cap + one pass interval`; defaults 3 x 6 + 30 + 1 = 49 s.

Places changed:
- docs/adr/0017-assignment-round-robin.md, note 25 (formula, one sentence on why the lag is added, 49 s).
- docs/agents/handoffs/P4-08d.md, F-R1 line (formula, 49 s).
- reports/claude/P4-08d-fix1.md, worst-case line (formula, 49 s).
The FS-19 table row states no number, so it is unchanged. No other place states 48 s or the old formula. No Go file, PROJECT_STATE.md or allowlist change.

Gates: gofmt clean (0 files), make test-docs rc 0. Tip before this report commit: 7a1d727.
