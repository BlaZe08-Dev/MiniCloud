# Workflow, Task Graph, and Execution Manifest

Status: designed, not executed. Slice V4-1 (decisions OD-10, OD-11, OD-12 answered in V4-0).

## Separate workflow from execution job

A **workflow** is the user's complete request. A **workflow task** is one independently schedulable unit in that request. A workflow contains a directed acyclic graph (DAG) of tasks and dependencies.

Keep the existing V1 `Job`/`Attempt` lifecycle as the durable execution foundation. A workflow task should map to one or more normal execution jobs/attempts rather than inventing a second, inconsistent lease/retry system.

The exact relational mapping and migration are implementation decisions requiring an ADR amendment and schema review.

## Workflow lifecycle

Suggested workflow states:
- `DRAFT`
- `AWAITING_APPROVAL`
- `READY`
- `RUNNING`
- `PARTIALLY_COMPLETED`
- `SUCCEEDED`
- `FAILED`
- `CANCEL_REQUESTED`
- `CANCELLED`

Suggested task states should map closely to existing job states where possible. Avoid duplicating state machines without a clear need.

A workflow is `SUCCEEDED` only when all required tasks have succeeded and all required artifacts have been committed and verified.

## Task manifest

Each task manifest should include:

- immutable workflow ID and workflow revision
- stable task ID/key
- human-readable objective
- bounded instruction/prompt reference
- dependency task IDs
- required input artifact IDs/checksums
- expected output artifact contract
- required capability labels
- minimum/maximum resource envelope
- preferred adapter/tool family (optional)
- allowed model preferences (optional)
- workspace snapshot/revision reference
- execution profile and approval requirements
- timeout and retry policy
- idempotency/replay policy
- priority/deadline if supported

The manifest must be versioned and validated. It must not contain provider credentials or raw host environment dumps.

## DAG rules

- The graph must be acyclic.
- Every dependency must reference an existing task in the same workflow revision.
- A task becomes schedulable only when all required dependencies have committed outputs.
- A failed dependency blocks or fails downstream tasks according to an explicit workflow policy.
- Independent tasks may run in parallel only when they have isolated workspaces and non-conflicting output contracts.
- Replanning creates a new immutable workflow revision; it does not silently rewrite the definition of a running attempt.

## Execution mapping

A ready workflow task is converted into an execution job carrying a typed execution specification. Existing job attempts still provide:
- attempt number and identity
- lease and renewal
- stale result rejection
- retry budget and failure classification
- cancellation and terminal-state rules
- durable audit events

AI CLI execution must be a registered execution kind/adapter, not an arbitrary task name accepted from a prompt.

## Idempotency

At-least-once execution remains the platform contract. An AI CLI may perform external side effects or produce non-deterministic output. Therefore:
- each attempt receives an immutable input snapshot and unique workspace
- retry starts from a defined snapshot, not a half-mutated prior attempt workspace
- only the active lease may commit the attempt result
- artifact commit is content-addressed and idempotent
- side effects outside the workspace require explicit policy/approval
- do not automatically repeat a non-idempotent external action without a defined replay rule

## Suggested schema concepts

Potential new tables/entities:
- `workflows`
- `workflow_revisions`
- `workflow_tasks`
- `workflow_dependencies`
- `workflow_task_jobs` or an equivalent task-to-job link
- node tables per the device model D3 (not workflow tables; listed for completeness): `nodes`, `node_hardware`, `node_telemetry` (V2-1) and `node_tools` (V4-3); see [`NODE_CAPABILITIES.md`](NODE_CAPABILITIES.md)
- `workspaces`
- `artifacts`
- `artifact_references`
- `approvals`

These are conceptual names, not an approved migration specification. Check existing schema and lock ordering before choosing final tables. Migrations remain forward-only; the next free number is `000007` (`000006` is taken by V2-0b, ADR-0020), expected to be taken by V2-1.
