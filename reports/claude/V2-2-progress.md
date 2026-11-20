# V2-2 fix round 1 progress

Done and pushed: F-V2-2-01 guard fix and tests (d7cb6d2), F-V2-2-02 code change (n/a rendering, JSON `stats` is `{"n":0}`), F-V2-2-03/04 README notes.
Mutants: M1 killed by TestCheckDatabaseURL (the `?sslmode=disable&user=other` case); M2 killed by TestCheckDatabaseURL (the `?SSLMODE=disable` and no-sslmode cases).

Not done: the S2 re-run and replacing the s2-*-deaa293 files, the handoff update, and the final gates.
Reason: the load average would not settle below 0.7. After about 25 minutes of waiting it read 1.27 / 1.33 / 1.51. A Brave browser was using 40% or more of the CPU. `--max-load` was not raised.
Next step: close the browser, wait for 1/5/15-minute load below 0.7, run `make bench SCENARIO=s2 BENCH_OUT=<dir outside repo>` on a clean tree, copy the pair named for the new commit, update the handoff and README, then run the gates.
