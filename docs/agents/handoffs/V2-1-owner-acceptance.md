# V2-1 owner answers, round 2, and acceptance of ADR-0022

Recorded by task V2-1-ADR-FIX3. These are the owner's last answers on the V2-1 ADR (the earlier ones are in `V2-1-owner-answers.md`).

## OQ-8 (answered 2026-11-20, evening)

Keep the 60 s placeholder for `MINICLOUD_NODE_HARDWARE_MIN_INTERVAL`. It stays configurable. Revisit it after the OD-23 benchmark.

Carried out in ADR-0022 Section 4.1 and Section 9; recorded in the Section 16 answered table. The value stays labelled "placeholder, not measured".

## Q3 wording (answered 2026-11-20, evening)

Keep `sample_age_ms` plus the database receive time. No node-side timestamp. No node clock stored.

Carried out in ADR-0022 Section 3.3, label [Decided: owner confirmation, 2026-11-20].

## Acceptance (answered 2026-11-21, after the second audit)

The owner accepts ADR-0022.

ADR-0022 Status is now "Accepted (2026-11-21, owner decision)". It is still design only. Each implementation slice (V2-1a to V2-1e) needs its own owner-approved task entry before code.
