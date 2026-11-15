# Workspace and Artifact Service

Status: designed, not executed. Slice V4-1 (single machine first); cross-node transfer in V4-6.

## Problem

Multiple AI CLI agents must not edit the same live working directory concurrently. A shared checkout creates race conditions, overwritten files, inconsistent tests, and unreviewable results.

## Workspace model

- Every task attempt receives an isolated workspace.
- The workspace is created from an immutable repository revision, snapshot, or parent-task artifact.
- The workspace is scoped to one node and one attempt unless an explicit handoff creates a new immutable snapshot.
- The Node Agent enforces filesystem boundaries; a prompt instruction alone is not a security boundary.
- Concurrent tasks must use separate worktrees/directories and explicit output ownership.
- The coordinator's own checkout must never be the default shared execution workspace.

For Git-backed projects, a Git worktree or equivalent snapshot mechanism is a reasonable first implementation. Do not assume every workload is a Git repository; define a workspace provider interface.

## Artifact model

Artifacts may include:
- Git patches/commits
- source files or generated files
- test reports
- logs
- build outputs
- structured JSON results
- images or other binary outputs where permitted

Each artifact needs:
- stable artifact ID
- content checksum (SHA-256 or stronger approved digest)
- size and media type
- producing workflow/task/attempt
- source workspace snapshot
- creation time
- retention policy
- access-control metadata

Store artifact metadata in PostgreSQL. Store large bytes in a bounded artifact store (local filesystem for an initial single-machine implementation, with an interface for future object storage). Do not put large binary payloads into the V1 `jobs.payload`, `results.output`, or `job_events` fields.

## Transfer and dependency rules

- Transfer artifacts by immutable reference and checksum.
- Verify checksum and size on receipt.
- A dependent task starts only after required artifacts are committed and verified.
- A node must not trust a filename or path supplied by another node without validating it against the artifact manifest.
- Prevent path traversal, symlink escapes, archive bombs, and unsafe file permissions.
- Keep artifact download authorization separate from job visibility.

## Integration

A workflow integration step collects task artifacts and produces a proposed combined result:
- apply patches only to a designated integration workspace
- detect overlapping file modifications
- run configured validation/tests
- report conflicts rather than silently choosing one agent's output
- require human approval before merging into a protected branch or changing a user's main checkout

## Logs and output limits

V1's 64 KiB result output limit remains unchanged for existing jobs. AI CLI logs and artifacts require a separate streaming/bounded storage design with explicit per-task, per-workflow, and per-node quotas. Do not silently increase V1 limits.
