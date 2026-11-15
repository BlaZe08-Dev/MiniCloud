# MiniCloud Interfaces: API, CLI, SDK, Chat, and External AI Agents

Status: designed, not executed. Slice V4-5 (decisions OD-01, OD-15, OD-16 answered first). The V1 interfaces today are the `/v1` HTTP API and `mc submit/get/attempts/workers`.

All interfaces must use the same workflow/job backend and authorization model. Do not implement separate business logic in the UI.

## 1. HTTP API

Keep existing `/v1/jobs`, worker protocol, and response contracts backward compatible.

Additive workflow/node/tool endpoints can be introduced under `/v1` when they do not break existing semantics. A new API version is required if a future contract is breaking. Candidate resources:

- `POST /v1/workflows`
- `GET /v1/workflows/{workflow_id}`
- `GET /v1/workflows/{workflow_id}/tasks`
- `POST /v1/workflows/{workflow_id}/cancel`
- `POST /v1/workflows/{workflow_id}/approve`
- `GET /v1/nodes`
- `GET /v1/nodes/{node_id}`
- `GET /v1/nodes/{node_id}/tools`
- artifact metadata/download endpoints with authorization

These are proposed names, not implemented routes. Final request/response schemas require an API contract review.

## 2. CLI

Retain existing `mc submit/get/attempts/workers`. Add workflow-oriented commands in a compatible way, for example:

- `mc workflow submit --prompt-file ...`
- `mc workflow plan ...`
- `mc workflow get <id>`
- `mc workflow tasks <id>`
- `mc workflow approve <id>`
- `mc workflow cancel <id>`
- `mc nodes`
- `mc node inspect <id>`
- `mc tools`

Exact command names and UX should be settled in the CLI task. CLI must not bypass API authorization or scheduler validation.

## 3. SDK

Provide a typed SDK over the HTTP API. Start with Go because the platform is Go, then add other languages only when there is a concrete client need. SDKs must not connect directly to PostgreSQL.

## 4. MiniCloud chat UI

The UI is deliberately small:
- submit a request in natural language
- preview the generated task plan
- approve/reject a plan
- monitor workflow and task states
- inspect node/tool readiness
- view bounded logs and artifact links
- cancel workflows/tasks where permitted

It is not:
- a code editor
- an IDE
- a general-purpose autonomous coding agent
- a replacement for Claude Code, Codex, or Gemini

The UI is a client of the same authenticated API, not a privileged backend.

## 5. External primary AI agent integration

The user may run a preferred primary AI CLI on the coordinator device. MiniCloud must not assume it can observe that process or intercept its output automatically.

Provide an explicit ingress contract so the primary agent can submit a request or structured task plan. Possible integration mechanisms include:
- direct HTTP API calls
- `mc` CLI invocation
- SDK
- an optional MCP server/tool adapter

MCP is a candidate integration, not a committed dependency. Confirm the primary agent's supported tool integration and the desired approval flow before choosing it.

The external primary agent is not the same component as MiniCloud's Intelligence Dispatcher. The primary agent sends the request; MiniCloud's planner validates/decomposes it and its deterministic scheduler places tasks.
