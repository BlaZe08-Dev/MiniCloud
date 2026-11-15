Task: V2-0 independent audit (Opus, read-only on production code)
Branch: agent/claude/V2-0-audit (from agent/claude/V2-0 at 7273571)
Work tip (last commit before this report): 39b543c
Date: 2026-11-16

## Final message

**V2-0 audit verdict: APPROVE WITH FIXES.** No blocker or major finding, four minor ones. Full detail is in docs/agents/reviews/V2-0.md, with evidence in docs/agents/reviews/V2-0-evidence/.

- **Gates (own DB) match the builder:** unset 412/91/0, race 412/91/0 with 0 data races, db 163/0/0, security 75/0/0. gofmt, vet, build and test-docs are clean. New tests at `-race -count=50`: 550/0/0.
- **Real binaries:** under fifo_pull with 4 slots, 8×5 s jobs reached max 4 concurrent in 10.30 s. Under round_robin it was 1 concurrent and 46.60 s, the same as slots=1 (46.63 s; fifo slots=1 took 40.41 s). A full worker sent 0 polls. SIGTERM with 4 running: exit in 3 ms, leases expire to LOST, jobs go to RETRYING, 0 results, no double report. Both mixed-version pairs complete their jobs.
- **Attacks that held:** slots never exceeded, never negative, never leaked (panics, 500s for the whole delivery budget, renew verdicts racing completion, shutdown racing a poll). Config values 0 to "abc" behave as claimed.
- **A-1:** mutant "skip heartbeatWG.Wait" survives all 11 builder tests; only my audit test kills it. Builder mutants a, b and c reproduce (8, 9 and 3 builder tests fail), and my "release before delivery" mutant is killed.
- **A-2:** an unrecovered panic in the renewal, delivery or heartbeat goroutine now kills up to TotalSlots running attempts. V1 lost 1. No reachable trigger was found.
- **A-3:** stale "one at a time" docs: README:33, architecture.md:167, NODE_CAPABILITIES.md:116.
- **A-4:** ADR §4 "only differences" omits the panic change. The shutdown bound is ≤3 s pollSleep plus 60 s.
- **Fixes:** cherry-pick the heartbeat-wait test, recover or document agent goroutine panics, and correct the three docs plus ADR §4/§2.5.
