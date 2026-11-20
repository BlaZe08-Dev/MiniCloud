Q1. How many historical hardware records should MiniCloud keep per node?
Accept: 10 records
Decision: Keep the latest 10 hardware inventory records per node.

Why:
- Allows us to track hardware changes over time.
- Prevents the database from growing unnecessarily.
- Makes hardware history easy to inspect.
Additional improvement: Don't create a new record if the hardware information hasn't changed. Validate and rate-limit updates to prevent unnecessary database writes.
Q2. Should node reporting be enabled by default or opt-in?
Accept: Opt-in
Decision: Node reporting should be explicitly enabled by the user.

Why:
- Preserves existing worker behavior.
- Avoids collecting hardware information without the user's intention.
- Gives users control over node reporting.
Implementation recommendation:
- Use an explicit node_reporting_enabled configuration.
- Allow a configurable state directory, with ~/.minicloud as the default when reporting is enabled.
- Do not use the existence of MINICLOUD_NODE_STATE_DIR alone as proof of consent.
Q3. How frequently should workers report resource telemetry?
Accept: Configurable, benchmark first
Decision: Make the reporting interval configurable. Select the default after benchmarking through OD-23.

Why:
- Frequent reporting creates unnecessary network and database overhead.
- Infrequent reporting gives the scheduler outdated resource information.
- Different machines and workloads have different requirements.
Additional requirements:
- Every report should include a timestamp and sequence number.
- Reject out-of-order telemetry.
- Mark old telemetry as stale or unknown.
- Never treat telemetry as a guaranteed resource reservation.
Q4. Should Linux resource detection include GPU detection in V2-1?
Accept: Defer GPU detection
Decision: Defer GPU detection to a later milestone.

Why:
- GPU detection introduces vendor-specific complexity.
- NVIDIA, AMD and Intel have different detection mechanisms.
- It adds maintenance and testing requirements.
Implementation recommendation:
- Keep the resource schema extensible for future GPU support.
- Report GPU information as unknown when detection is unsupported.
- Do not report 0 GPUs unless the system has actually confirmed that no supported GPU is present.
Q5. How much CPU and RAM should MiniCloud leave available for the operating system?
Improve: Dynamic resource headroom
Decision: Do not use a fixed reserve such as 1 GB RAM for every machine. Introduce OS-aware dynamic resource headroom.

Why:
- A 16 GB machine does not necessarily have 16 GB available for jobs.
- Operating systems and background applications continuously consume resources.
- Available RAM changes while jobs are running.
- CPU capacity also changes dynamically depending on system activity.
Implementation recommendation:
- Linux: use OS-reported available memory, particularly MemAvailable.
- Windows: use GlobalMemoryStatusEx or an equivalent OS-supported API.
- CPU: monitor utilization, logical cores and system pressure.
- Account for existing MiniCloud reservations.
- Support configurable minimum RAM reserves and percentage-based reserves.
- Use runtime monitoring to react to changing resource pressure.
Example configuration:
resources:
  memory:
    strategy: dynamic
    min_free_reserve_mb: 2048
    reserve_percent: 15

  cpu:
    strategy: pressure_aware
    max_utilization_percent: 80
    min_idle_cores: 1


These are example starting values, not universal defaults. Benchmark and tune them.

Milestone boundary:
- V2-1: Define the resource-capacity model and configuration.
- V2-3: Implement resource reservations, dynamic headroom and enforcement.
- The worker makes the final decision before starting a job; the coordinator's telemetry is only an estimate.
Q6. Should GET /v1/nodes and node IDs in GET /v1/workers be available before authentication in V3-2?
Modify: Local-only access
Decision: Permit these endpoints in a local-only development configuration, but do not expose unauthenticated node-management endpoints to network clients.

Why:
- Node IDs are not passwords or authentication credentials.
- Exposing hardware information can reveal useful details about the infrastructure.
- Knowing a node ID does not prove that a machine owns that identity.
- Localhost binding is a network restriction, not an authentication mechanism.
Implementation recommendation:
- Keep local development access available.
- Restrict sensitive hardware information.
- Require authentication and authorization before enabling remote node-management endpoints.
- Never treat UUIDs as proof of node identity.
Q7. Should node impersonation risk have its own security finding (SEC-070), or remain under SEC-004?
Accept: Create SEC-070
Decision: Create a separate SEC-070 finding and link it to SEC-004.

Why:
- SEC-004 concerns missing API authentication.
- SEC-070 focuses on node identity impersonation.
- Keeping them separate makes remediation and security tracking clearer.
Suggested severity: Low while node reporting is controlled and the coordinator is local-only. Reassess severity if remote unauthenticated node registration becomes possible.

Final decision summary
Question	Final decision
Q1. Hardware history	Keep 10 records
Q2. Node reporting	Explicit opt-in
Q3. Telemetry interval	Configurable; benchmark default
Q4. GPU detection	Defer; report unknown
Q5. CPU/RAM reserve	Dynamic, OS-aware headroom
Q6. Node endpoints	Local-only before authentication
Q7. Impersonation risk	Separate SEC-070 linked to SEC-004

One architectural principle to preserve
MiniCloud must distinguish three separate concepts:
1. Identity: Which physical machine is this?
2. Observation: What resources does the machine currently report?
3. Authority: Is this machine authenticated and trusted to participate?
A UUID identifies a node; telemetry describes its observed state; authentication establishes trust. These must never be treated as interchangeable.