# Intelligence Dispatcher Specification

Status: designed, not executed. Slice V4-4 (decisions OD-02, OD-03 answered first).

## Purpose

Convert a user's natural-language request into a typed workflow plan that MiniCloud can validate, persist, schedule, and monitor.

The dispatcher is a planner, not a worker and not the authority for cluster state.

## Inputs

- Natural-language request or structured request from an external primary AI agent.
- User constraints: deadline, preferred tools/models, allowed nodes, approval mode, budget/cost policy if configured.
- Project/workspace metadata: repository identity, immutable revision or snapshot, permitted paths, task context.
- Current capability catalogue: nodes, hardware, installed adapters, tool versions, readiness, and live resource telemetry.

## Output contract

The planner must return schema-valid structured data, not free-form execution commands. At minimum:

- workflow title and user-visible summary
- ordered task identifiers
- task objective and bounded instructions
- dependency edges
- required capabilities and resource envelope
- preferred tool family/model only as a preference
- workspace input reference
- expected artifacts and output contract
- timeout and retry policy proposal
- risk classification and required approval gates

The planner output is untrusted input. Validate it as if it came from an external client.

## Planning pipeline

1. Normalize request and extract constraints.
2. Identify whether the request is one task or a multi-task workflow.
3. Decompose only when parallelism or dependency separation is useful.
4. Build a DAG; reject cycles and missing dependencies.
5. Define artifact contracts between tasks.
6. Estimate resource requirements conservatively.
7. Recommend tool families based on task type and available tool capabilities.
8. Emit a structured plan.
9. Validate schema, policy, size limits, task count, dependency graph, and permissions.
10. Present the plan for approval when required.
11. Persist the approved workflow and enqueue ready tasks.

## Task decomposition rules

- Prefer a small number of meaningful tasks over artificial fragmentation.
- Separate independent tasks only when they can safely work on isolated snapshots.
- Model dependencies explicitly; do not rely on prompt wording to imply execution order.
- A task that consumes another task's output must reference a committed artifact ID/checksum.
- Avoid assigning two tasks to modify the same workspace branch concurrently.
- Do not infer that frontend work is always lighter than backend work or vice versa.
- Do not infer GPU requirement from the fact that a task uses an AI CLI.
- Ask for human clarification when requirements are materially ambiguous; do not invent project goals.

## Tool selection

The planner may propose:
- a provider family (Claude/Codex/Gemini/other registered adapter)
- a model preference supported by that adapter
- a capability label such as coding, reasoning, test execution, documentation, or local inference

The deterministic scheduler performs final eligibility and placement using actual node reports. A planner recommendation is never a reservation.

## Safety and reliability

- The planner cannot choose an unregistered executable path.
- The planner cannot request unrestricted host access.
- The planner cannot supply raw shell scripts to be executed directly.
- Tool/model names and capability claims are validated against registered adapter metadata.
- Every plan revision receives an immutable version/hash.
- Replanning does not mutate already-running tasks; it creates a new workflow revision or explicit task replacement with audit events.
- Planner failure must not corrupt existing workflows. It may return a typed planning failure or request clarification.
- Do not silently re-run a completed task with external side effects.

## Model/provider abstraction

Do not bind the system to one LLM vendor in the domain model. A planner-provider interface may be implemented behind the dispatcher. Provider credentials and billing policy are configuration concerns and must never be copied into worker assignments or logs.

The planner's model provider is an open decision; see `OPEN_DECISIONS.md`.
