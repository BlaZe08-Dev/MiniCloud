# ADR 0002: Go Standard Library net/http for Initial API

## Status
Accepted

## Context
MiniCloud requires an HTTP API for client job submission, status querying, worker registration, heartbeats, and result reporting. We need to decide whether to use a third-party web framework (such as Gin, Echo, or Chi) or Go's standard library `net/http` (specifically Go 1.22+ enhanced pattern routing with method and wildcard matching).

## Decision
We choose the Go standard library `net/http` with zero third-party routing dependencies for Milestone 1 and early phases.

Since Go 1.22, `http.ServeMux` natively supports HTTP method matching (e.g., `POST /v1/jobs`) and path variables (e.g., `GET /v1/jobs/{id}` via `req.PathValue("id")`), eliminating the primary reason historically cited for third-party routers.

## Consequences
- **Positive:**
  - Zero external dependencies; minimal dependency supply-chain risk and attack surface.
  - Idiomatic, clean, and forward-compatible with standard `http.Handler` interfaces.
  - Low compilation overhead and zero external API deprecation concerns.
- **Negative:**
  - Middleware chaining and structured JSON serialization helpers must be written manually using lightweight standard library wrappers.

## Alternatives Considered
- *Chi Router:* Lightweight and idiomatic, but introduces an external dependency when Go 1.22 stdlib `http.ServeMux` already provides required method and path variable routing.
- *Gin / Echo:* Feature-heavy frameworks with non-standard context wrappers that obscure core HTTP mechanics and add unnecessary dependencies.
- *gRPC:* Premature for Milestone 1; adds protobuf compilation complexity before HTTP contracts stabilize. Considered as an option for Phase 7+.
