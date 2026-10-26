# Security, Adversarial Testing, and Reliability Principles

## Purpose and Scope

MiniCloud treats security, reliability, and performance as core architectural requirements, not afterthoughts. These principles apply to every phase, milestone, code change, pull request, and operational deployment. They extend the project blueprint and engineering instructions with adversarial testing practices.

## Adversarial Engineering Roles

Every feature and milestone must be evaluated through five specialized perspectives:

1. **Application Security Engineer**: Audits input validation, API contracts, boundary sanitization, authorization enforcement, secret handling, and error response leakage.
2. **Authorized Penetration Tester**: Deliberately probes for exploit primitives, authentication bypasses, state corruption, SQL injection, parameter tampering, and request smuggling within authorized local environments.
3. **Distributed-Systems Reliability Engineer**: Evaluates independent component failure modes, network partitions, split-brain conditions, clock skew, zombie worker execution, lease expirations, and crash recovery.
4. **Adversarial QA Engineer**: Formulates pathological edge cases, boundary conditions, malformed serialization payloads, concurrency race hazards, and out-of-order event flows.
5. **Performance and Load Testing Engineer**: Formulates repeatable stress workloads, benchmarks baseline throughput and tail latencies, measures queue saturation, and evaluates backpressure mechanisms.

## Break-Before-Fix Workflow

When testing or reviewing a feature, follow an eight-stage sequence:

$$\text{Understand} \longrightarrow \text{Threat Model} \longrightarrow \text{Attack} \longrightarrow \text{Observe} \longrightarrow \text{Report} \longrightarrow \text{Recommend} \longrightarrow \text{Fix (Authorized)} \longrightarrow \text{Retest}$$

1. **Understand**: Clarify target requirements, intended invariants, and explicit design limits.
2. **Threat Model**: Map component assets, untrusted inputs, trust boundaries, and failure assumptions.
3. **Attack**: Design targeted test vectors (valid, malformed, out-of-order, stress, fault injection).
4. **Observe**: Capture unvarnished system behavior, logs, metrics, return codes, and database state.
5. **Report**: Disclose the failure in the findings register with reproduction steps and root cause.
6. **Recommend**: Propose concrete remediation strategies, architectural trade-offs, and regression tests.
7. **Fix (Only When Authorized)**: Implement remediation only after reporting. In an active slice, fix and disclose; for merged code, obtain authorization before modifying implementation.
8. **Retest**: Verify the fix against the reproduction test and confirm no regressions in existing suites.

## Evidence Labels and Honesty Rules

Honesty and precise evidence classification are non-negotiable. Every statement regarding tests and security posture must use one of these explicit classifications:

- **executed**: The test was actively executed in the local environment and the exact outcome observed.
- **designed-not-executed**: The test case or attack vector has been architected or planned, but has not yet run.
- **confirmed**: The vulnerability, bug, or invariant is verified by a concrete failing test or reproducible trace.
- **inferred from code review**: The risk or property is deduced from static analysis or code inspection without dynamic test confirmation.
- **open question**: A potential risk or behavioral uncertainty requiring further investigation or architectural decision.

Never report simulated or assumed results as actual results. Never claim a test passed if it was not run. Never assert a performance or capacity claim without empirical measurement on local hardware.

## Testing Integrity Rules

- **No Silent Fixes**: Never patch a vulnerability or behavioral bug quietly. Disclose the failure and root cause in the milestone report and findings register.
- **Never Weaken Tests**: Never delete existing tests, skip checks, loosen tolerances, or weaken assertions to make a test pass.
- **Preserve Failure Evidence**: Keep reproduction tests and failing traces documented so regressions can be prevented.
- **Production and External Safety**: Never execute uncontrolled load tests or denial-of-service simulations against production systems or third-party networks.

## Repeatability and Tooling

- **Automated Regressions**: Every fixed security issue or distributed failure must be backed by an automated regression test.
- **Standard Library Tooling**: Leverage Go standard library tooling without friction: `-race` detector, Go native fuzzing (`testing.F`), and `go vet`.
- **Approved External Tooling**: Introduction of external analysis or load-testing dependencies requires an explicit Architectural Decision Record (ADR) and approval. Initial load testing must use a plain Go load generator.
- **Secret Redaction**: Never print credentials, tokens, connection strings, or sensitive lease values to terminal logs, test output, or committed documents.

## Documentation Citation & Verification Rules

To prevent fabricated test output, invalid file citations, broken line anchors, or nonexistent ADR references (as disclosed in SEC-018), MiniCloud enforces automated documentation verification (`make test-docs` / `go test ./tests/docs`):

- **Automated Verification Rules**:
  - **Repo Paths (Rule A)**: Every cited repository path (`internal`, `cmd`, `tests`, `deployments`, `docs`, or `migrations` ending in `.go`, `.md`, `.yml`, `.sql`) must exist. Relative links are resolved from the document's own directory.
  - **Line Anchors (Rule B)**: Every line anchor (`path:line`, `path#L123`, `path#L10-L20`) must reference a valid line number within the target file bounds.
  - **Test Identifiers (Rule C)**: Every cited test identifier (`Test[A-Z]...`) must exist in the test suite (collected by parsing `*_test.go` ASTs), unless the line contains an explicit exemption keyword (`proposed`, `future`, `planned`, `designed, not executed`, `does not exist`, `nonexistent`, `invented`).
  - **Forbidden Strings (Rule D)**: Local file URI schemes (such as `file:` triple-slash) and legacy route prefixes (such as `/api` preceding `/v1`) are strictly prohibited in documentation.
  - **ADR Citations (Rule E)**: Every cited ADR number (`ADR-00NN` or `ADR NN`) must map to an existing `docs/adr/00NN-*.md` record.
- **Allowlist Rules (`tests/docs/citation_allowlist.txt`)**:
  - Format: `kind|value|reason` where `kind` is `path`, `test`, or `string`.
  - **Mandatory Justification**: Every entry must include a concrete reason of at least 15 characters explaining why suppression is legitimate.
  - **Zero Stale Entries**: The test suite fails if an allowlist entry becomes stale—either because the value is no longer cited anywhere in the scanned documentation, or because it now exists in the repository or test suite. This guarantees the allowlist cannot grow silently or mask resolved issues.

## Milestone Focus Areas

Adversarial testing progresses across milestones in alignment with architectural staging:

- **V1 (Core Correctness & In-Process Safety)**:
  - Strict FSM transitions, monotonic attempts, at-least-once semantics under retry.
  - PostgreSQL transaction isolation, lock contention, durable lease handling, worker heartbeats, and reaper correctness.
  - Input size bounds (payload/output $\le 64\text{ KiB}$), API body limits ($\le 1\text{ MiB}$), header timeouts.
  - Safe in-process allowlisted tasks; verification of zero shell command execution on host.
- **V2 (Scheduling, Queueing & Dispatch)**:
  - Resource-aware scheduling boundaries and priority starvation.
  - Dispatch race conditions and duplicate delivery under Redis Streams (evaluated against PostgreSQL baseline).
  - Queue backlog spikes, worker saturation, and worker disconnection during dispatch.
- **V3 (Container Isolation & Multi-Machine Hardening)**:
  - Docker container execution isolation, cgroup constraints, privilege dropping, filesystem write barriers.
  - Mutual TLS (mTLS) and cryptographic token authentication for remote worker-coordinator communication.
  - Network split, packet drop, and multi-host partition recovery.
