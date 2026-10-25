# ADR 0012: Security, Adversarial Testing, and Reliability Workflow

## Status
Accepted

## Context
MiniCloud is a distributed job execution platform where correctness, consistency, and reliability under concurrent failures are paramount. As demonstrated in Milestone 3 (PostgreSQL persistence, concurrency handling, and lease coordination), subtle race hazards, logging misattributions, and edge-case precedence discrepancies can emerge across independent components.

To prevent silent regressions, unverified assumptions, and premature declarations of feature completeness, the project owner established persistent security and reliability instructions ([MINICLOUD_SECURITY_INSTRUCTIONS.md](../source/MINICLOUD_SECURITY_INSTRUCTIONS.md)). This ADR reconciles the operational interaction between software engineering workflows and adversarial security testing practices, establishing clear policies for tooling, reporting thresholds, failure disclosures, and milestone gates.

## Decision

We adopt the following engineering and adversarial testing policies across all MiniCloud milestones:

### 1. Scope of the Break-Before-Fix Workflow
- **Merged Code & Discovered Defects**: When testing already-merged components or running adversarial evaluations, the full sequence ($\text{Understand} \to \text{Threat Model} \to \text{Attack} \to \text{Observe} \to \text{Report} \to \text{Recommend} \to \text{Fix} \to \text{Retest}$) is mandatory. Fixes must not be implemented until the finding is documented and authorized.
- **In-Progress Development Slices**: Deficiencies or bugs discovered within the code of an active, unmerged slice must be resolved directly on the slice branch before completion. However, **silent fixes are prohibited**: all such discoveries, reproduction tests, and resolutions must be explicitly disclosed with evidence in the slice's final report and logged in the findings register.

### 2. Tooling and Dependency Approvals
- **Standard Library Tooling**: Standard Go tooling requires no prior approval and must be used continuously:
  - Concurrency data race detector: `go test -race`
  - Static analysis: `go vet`
  - Native fuzz testing: `go test -fuzz` (`testing.F`)
- **External Security and Quality Tools**: Third-party scanners or linters (e.g., `govulncheck`, `staticcheck`) and external load tools require explicit owner approval and a dedicated ADR prior to adoption.
- **Load Generation**: The initial workload and load generator must be authored in pure standard library Go within the repository, maintaining zero external dependencies.

### 3. Tiered Reporting Format
- **Medium, High, and Critical Findings**: Must use the full structured report format specified in [security-testing.md](../security/security-testing.md) (Finding, Severity, Affected Component, Preconditions, Reproduction Steps, Evidence, Impact, Root Cause, Recommended Fix, Regression Test, Status).
- **Low and Informational Findings**: Are tracked concisely in the tabular summary register of [findings.md](../security/findings.md) with ID, severity, status, component, summary, and regression test reference.

### 4. Controlled Local Load Testing and Baseline Rigor
- **Environment Boundaries**: All load and stress testing must be strictly controlled and bounded by the physical hardware capacity of the local development machine.
- **No Invented Numbers**: Throughput (jobs/sec), latency percentiles (p50, p95, p99), and queue wait times must reflect actual observed measurements. Never project or invent benchmark figures.
- **Baseline Requirement**: No architectural performance or superiority claims may be asserted without a measured baseline (e.g., V2 Redis Streams dispatch must be evaluated directly against the V1 PostgreSQL baseline).
- **Safety Restriction**: Uncontrolled load generation against production environments or third-party networks is strictly prohibited.

### 5. Responsible Disclosure and Embargo for Public Repositories
- If the repository is public or shared, actionable exploit details, proof-of-concept scripts, or reproduction payloads for any **unfixed High or Critical** vulnerability must not be committed to Git history.
- Instead, a placeholder entry (ID, Severity, Affected Component, and status `details withheld, reported to owner`) is recorded in the register, and full technical details are communicated out-of-band to the project owner until remediation is approved and verified.

### 6. Accepted Version Boundaries and Gaps
- **Authentication & Transport Security**: As documented in [ADR 0004](0004-version-boundaries-and-compatibility.md), the absence of HTTP API authentication, authorization, and TLS is an explicitly accepted architectural gap for V1 (cataloged as SEC-004).
- **Current Mitigation**: Sockets bind strictly to `127.0.0.1` by default. Full worker mutual TLS (mTLS) and token authentication are deferred to Milestone V3.

### 7. Mandatory Slice Security Review Section
- Every engineering slice completion report must conclude with a dedicated **Security Review** section containing:
  1. Assumptions challenged during testing.
  2. Malformed inputs, edge cases, and attack vectors attempted (citing exact test names).
  3. Clear statement of what was **NOT** tested.
  4. Catalog of findings with accurate evidence labels (`executed`, `designed-not-executed`, `confirmed`, `inferred from code review`, `open question`).

### 8. Verification of Tool and Agent Claims
- Coverage claims, static analysis metrics, and AST verification assurances reported by automated tools or AI agents must be verified against actual source code or mutation tests before being recorded as factual (preventing process regressions such as SEC-005).

## Consequences

### Positive
- Prevents regressions and untested happy-path assumptions from reaching production branches.
- Establishes traceable accountability for all architectural compromises and accepted risks.
- Maintains a clean, dependency-free development loop centered on standard Go tooling.
- Ensures truthful, verifiable performance and reliability baselines.

### Negative
- Increases documentation and reporting overhead for every feature slice.
- Requires deliberate authoring of adversarial tests and fault injection scenarios for all distributed transitions.

## Alternatives Considered
- **All-in-one Heavy Tooling Suite**: Using heavy external frameworks (e.g., k6, Locust, SonarQube) from day one. Rejected because it violates the standard-library-first dependency rule (ADR-0004) and adds deployment complexity before core state transitions are stabilized.
- **Uniform Full Report for All Findings**: Requiring the 11-field structured report for trivial typos, informational config hints, and minor lint warnings. Rejected due to reporting fatigue; the tiered threshold (full report for Medium+, single line for Low/Info) preserves focus on genuine risks.
