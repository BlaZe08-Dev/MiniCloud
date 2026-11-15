# Universal CLI Adapter System

Status: designed, not executed. Slice V4-2 (one provider, single machine; decisions OD-04, OD-05, OD-14 answered in V4-0).

## Goal

Invoke AI CLI applications installed on each node through a common MiniCloud contract while preserving provider-specific behavior behind adapters.

Initial examples:
- Claude Code CLI
- OpenAI Codex CLI
- Gemini CLI
- future registered tools
- explicitly approved local scripts/tasks

This is an adapter system, not a generic shell execution API.

## Tools are not workers (owner decision D3)

A worker is the MiniCloud execution agent. An AI CLI is a tool that a worker launches through an adapter during an attempt, inside that attempt's lease. Starting a CLI never creates a worker, and a CLI never registers with the coordinator. Tool inventory and readiness are recorded per node (`node_tools`, V4-3).

## Trust model (owner decision D2)

AI CLI tasks may run on the host only for the owner's own projects, under a dedicated restricted OS account, inside a per-task workspace, with the read-only profile first; file changes need approval. Any other user's or untrusted workload waits for Docker isolation (V3-1). The CLI's own sandbox or permission flags are an additional layer, never the only boundary.

## Adapter contract

Each adapter should implement equivalents of:

- `Discover(ctx)`: locate the executable without guessing paths.
- `Version(ctx)`: return normalized tool/version metadata.
- `ProbeReadiness(ctx)`: return a readiness enum without exposing credentials.
- `Capabilities()`: report supported execution modes and task capability labels.
- `Validate(request)`: reject unsupported options before process launch.
- `Start(ctx, request)`: launch a process and return a supervised execution handle.
- `Wait(ctx, handle)`: collect completion status and bounded output.
- `Cancel(ctx, handle)`: terminate the process tree according to platform rules.
- `CollectArtifacts(ctx, handle)`: produce a checksummed artifact manifest.

The concrete Go interfaces and error types must be designed in a separate implementation task. Keep provider-specific code out of `internal/domain`.

## Process invocation rules

- Resolve an executable through a configured/validated path or trusted PATH lookup.
- Invoke the executable directly with an argument array.
- Never construct `sh -c`, `bash -c`, `cmd /c`, or PowerShell command strings from user/LLM input.
- Use `exec.CommandContext` or a platform-specific process supervisor with explicit cancellation and process-tree handling.
- Set a controlled working directory.
- Use an explicit environment allowlist; do not inherit arbitrary secrets from the Node Agent process.
- Bound runtime, memory/CPU where available, output/log volume, open files, and child processes.
- Capture stdout/stderr separately and redact sensitive values before persistence.
- Record executable path identity, adapter version, CLI version, invocation mode, exit code, and timestamps.
- Do not persist raw environment variables or credential material.

## Interactive versus noninteractive tools

The Node Agent must be able to start a CLI process when no terminal window is already open. It should use the tool's documented noninteractive/headless mode where available. It must not depend on opening a visible terminal or simulating keyboard input.

Provider command-line flags and behavior change over time. Each adapter must:
- detect supported CLI versions
- maintain tested invocation profiles
- fail closed when a required feature is unsupported
- have adapter-specific contract tests
- avoid assuming that all vendors share the same flags, approval model, output format, or cancellation behavior

Do not hardcode unverified provider flags in this architecture document. Verify them against the installed CLI version and official documentation during the adapter implementation task.

## Authentication

The CLI remains authenticated locally using its own supported mechanism. MiniCloud records only readiness metadata. It must not:
- read or export hidden credential stores
- upload tokens to the coordinator
- copy a user's provider credentials to another node
- print authentication output that may contain secrets

A tool that needs interactive login is `NEEDS_LOGIN`; it is not eligible for unattended execution until the user completes login locally.

## Execution profiles

Define explicit profiles, for example:
- `read_only_analysis`
- `workspace_edit`
- `test_execution`
- `script_execution` (disabled by default; separately approved)

Each profile defines permitted filesystem scope, network policy, tool approvals, timeout, resource limits, and whether human confirmation is required. A CLI's own permission mode is not a substitute for MiniCloud's node-level security boundary.

## Result contract

Return a normalized result containing:
- task/attempt identifiers
- adapter and CLI version
- exit status and normalized outcome
- bounded stdout/stderr references
- artifact manifest with checksums and sizes
- usage metadata when reliably available
- cancellation/timeout reason
- timestamps and redaction status

Never treat a process exit code alone as proof that the requested code changes are correct. Validation tasks and human review remain necessary.
