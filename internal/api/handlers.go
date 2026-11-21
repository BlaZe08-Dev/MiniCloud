package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/logsafe"
	"github.com/jackc/pgx/v5/pgconn"
)

func isConnectionOrTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// PostgreSQL Class 08 — Connection Exception, Class 57 — Operator Intervention (57014 statement_timeout, 57P01/2/3), or 55P03 lock_not_available
		if strings.HasPrefix(pgErr.Code, "08") || strings.HasPrefix(pgErr.Code, "57") || pgErr.Code == "55P03" {
			return true
		}
	}
	errMsg := strings.ToLower(err.Error())
	if strings.Contains(errMsg, "connection refused") ||
		strings.Contains(errMsg, "failed to connect") ||
		strings.Contains(errMsg, "dial tcp") ||
		strings.Contains(errMsg, "closed pool") ||
		strings.Contains(errMsg, "broken pipe") ||
		strings.Contains(errMsg, "connection reset") ||
		strings.Contains(errMsg, "driver: bad connection") {
		return true
	}
	return false
}

const maxIdentifierLength = 128

func isValidIdentifier(id string) bool {
	if len(id) == 0 || len(id) > maxIdentifierLength {
		return false
	}
	for i := 0; i < len(id); i++ {
		b := id[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' || b == '-' {
			continue
		}
		return false
	}
	return true
}

// ErrorResponse represents the standardized JSON error envelope.
type ErrorResponse struct {
	Error ErrorDetail `json:"error"`
}

type ErrorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

// writeJSON writes data as a JSON response.
//
// HTML escaping is off (SEC-067): with it on, < > & become 6-byte escapes and U+2028/U+2029
// double in size, so a valid payload under 64 KiB could reach the worker over the 64 KiB check
// in executor.Registry.Execute. The change is JSON-equivalent. X-Content-Type-Options: nosniff
// is the guard that keeps raw < and > safe in an application/json body. It is set here, before
// WriteHeader, and nowhere else in internal/api.
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(data)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	if status == http.StatusServiceUnavailable && code == "SERVICE_UNAVAILABLE" {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func mapDomainErrorToHTTP(err error) (status int, code string, msg string) {
	if isConnectionOrTimeoutError(err) {
		slog.Error("database outage or connection failure", logsafe.Err(err))
		return http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service temporarily unavailable"
	}

	switch {
	case errors.Is(err, domain.ErrJobNotFound):
		return http.StatusNotFound, "JOB_NOT_FOUND", err.Error()
	case errors.Is(err, domain.ErrAttemptNotFound):
		return http.StatusNotFound, "ATTEMPT_NOT_FOUND", err.Error()
	case errors.Is(err, domain.ErrWorkerNotFound):
		return http.StatusNotFound, "WORKER_NOT_FOUND", err.Error()
	case errors.Is(err, domain.ErrWorkerMismatch):
		return http.StatusForbidden, "WORKER_MISMATCH", err.Error()
	case errors.Is(err, domain.ErrLeaseExpired):
		return http.StatusConflict, "LEASE_EXPIRED", err.Error()
	case errors.Is(err, domain.ErrStaleAttempt):
		return http.StatusConflict, "STALE_ATTEMPT_REJECTED", err.Error()
	case errors.Is(err, domain.ErrJobAlreadyTerminal):
		return http.StatusConflict, "JOB_ALREADY_TERMINAL", err.Error()
	case errors.Is(err, domain.ErrUnknownTaskType):
		return http.StatusBadRequest, "UNKNOWN_TASK_TYPE", err.Error()
	case errors.Is(err, domain.ErrPayloadTooLarge):
		return http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", err.Error()
	case errors.Is(err, domain.ErrOutputTooLarge):
		return http.StatusBadRequest, "OUTPUT_TOO_LARGE", err.Error()
	case errors.Is(err, domain.ErrNoQueuedJobs):
		return http.StatusNoContent, "NO_QUEUED_JOBS", err.Error()
	case errors.Is(err, domain.ErrNoWorkersAvailable):
		return http.StatusServiceUnavailable, "NO_WORKERS_AVAILABLE", err.Error()
	case errors.Is(err, domain.ErrWorkerUnhealthy):
		return http.StatusServiceUnavailable, "WORKER_UNHEALTHY", err.Error()
	case errors.Is(err, domain.ErrInvalidRequest):
		return http.StatusBadRequest, "INVALID_REQUEST", err.Error()
	case errors.Is(err, domain.ErrJobNotQueued):
		return http.StatusConflict, "JOB_NOT_QUEUED", err.Error()
	case errors.Is(err, domain.ErrWorkerTaskUnsupported):
		return http.StatusBadRequest, "WORKER_TASK_UNSUPPORTED", err.Error()
	case errors.Is(err, domain.ErrWorkerNoCapacity):
		return http.StatusConflict, "WORKER_NO_CAPACITY", err.Error()
	// Node codes (ADR-0022 Sections 4.1 and 4.2). The 409 and 429 RATE_LIMITED bodies are opaque:
	// they do not say what is stored.
	case errors.Is(err, domain.ErrNodeNotFound):
		return http.StatusNotFound, "NODE_NOT_FOUND", "node not found"
	case errors.Is(err, domain.ErrTelemetryOutOfOrder):
		return http.StatusConflict, "TELEMETRY_OUT_OF_ORDER", "telemetry report out of order"
	case errors.Is(err, domain.ErrNodeRateLimited):
		return http.StatusTooManyRequests, "RATE_LIMITED", "node hardware update rate limited"
	case errors.Is(err, domain.ErrUnsupportedContractVersion):
		return http.StatusBadRequest, "UNSUPPORTED_CONTRACT_VERSION", "unsupported contract_version"
	case errors.Is(err, domain.ErrNodeLimitReached):
		return http.StatusTooManyRequests, "NODE_LIMIT_REACHED", "node limit reached"
	default:
		var invErr *domain.InvalidStateTransitionError
		if errors.As(err, &invErr) {
			return http.StatusBadRequest, "INVALID_STATE_TRANSITION", invErr.Error()
		}
		slog.Error("internal server error", logsafe.Err(err))
		return http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error"
	}
}

// Handler holds dependencies for all API endpoints.
type Handler struct {
	coord *coordinator.Service
	// localOnly mirrors Server.localOnly: node routes served, node_id visible and accepted.
	localOnly bool
}

func NewHandler(coord *coordinator.Service) *Handler {
	return &Handler{coord: coord}
}

// SubmitJobRequest defines the JSON schema for POST /v1/jobs.
// If max_attempts is omitted or null, it defaults to MINICLOUD_DEFAULT_MAX_ATTEMPTS (3 unless
// configured; valid range: 1..20).
// If timeout_seconds is omitted or null, it defaults to 60 (valid range: 1..86400).
type SubmitJobRequest struct {
	TaskType       string          `json:"task_type"`
	Payload        json.RawMessage `json:"payload"`
	TimeoutSeconds *int            `json:"timeout_seconds"`
	MaxAttempts    *int            `json:"max_attempts"`
}

func (h *Handler) HandleSubmitJob(w http.ResponseWriter, r *http.Request) {
	var req SubmitJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}

	if req.TaskType == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "task_type is required")
		return
	}

	maxAttempts := h.coord.DefaultMaxAttempts()
	if req.MaxAttempts != nil {
		if *req.MaxAttempts < coordinator.MinMaxAttempts || *req.MaxAttempts > coordinator.MaxMaxAttempts {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("max_attempts must be between %d and %d", coordinator.MinMaxAttempts, coordinator.MaxMaxAttempts))
			return
		}
		maxAttempts = *req.MaxAttempts
	}

	timeoutSec := coordinator.DefaultTimeoutSeconds
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < coordinator.MinTimeoutSeconds || *req.TimeoutSeconds > coordinator.MaxTimeoutSeconds {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", fmt.Sprintf("timeout_seconds must be between %d and %d", coordinator.MinTimeoutSeconds, coordinator.MaxTimeoutSeconds))
			return
		}
		timeoutSec = *req.TimeoutSeconds
	}

	job, err := h.coord.SubmitJob(r.Context(), req.TaskType, req.Payload, timeoutSec, maxAttempts)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if !isValidIdentifier(jobID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid job id")
		return
	}

	job, err := h.coord.GetJob(r.Context(), jobID)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) HandleListAttempts(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if !isValidIdentifier(jobID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid job id")
		return
	}

	attempts, err := h.coord.ListAttempts(r.Context(), jobID)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":   jobID,
		"attempts": attempts,
	})
}

func (h *Handler) HandleCancelJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if !isValidIdentifier(jobID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid job id")
		return
	}

	job, err := h.coord.CancelJob(r.Context(), jobID)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":  job.ID,
		"state":   job.State,
		"message": "cancellation processed",
	})
}

// workerWithNodeID is the local-only shape of a worker: node_id is always present, a string or
// null. The shallower NodeID field shadows the embedded omitempty one.
type workerWithNodeID struct {
	domain.Worker
	NodeID *string `json:"node_id"`
}

func (h *Handler) HandleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.coord.ListWorkers(r.Context())
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	// Copies are marshalled so a stored NodeID is stripped (not local-only: the V1 shape, key
	// absent, even if a row carries one after a rollback or a manual insert) or rendered as a string
	// or null (local-only) without touching the repository's values.
	out := make([]any, 0, len(workers))
	for _, wk := range workers {
		cp := *wk
		if h.localOnly {
			nid := cp.NodeID
			cp.NodeID = nil
			out = append(out, workerWithNodeID{Worker: cp, NodeID: nid})
		} else {
			cp.NodeID = nil
			out = append(out, cp)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workers": out,
	})
}

// registerWorkerRequest decodes the worker body with node_id kept as a raw JSON value, so a wrong
// type can never fail the whole request when the field must be ignored (audit A-01). The shallower
// RawNodeID shadows the embedded Worker.NodeID.
type registerWorkerRequest struct {
	domain.Worker
	RawNodeID json.RawMessage `json:"node_id"`
}

func (h *Handler) HandleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	var req registerWorkerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}
	worker := req.Worker
	worker.NodeID = nil

	if !isValidIdentifier(worker.ID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}

	// PostgreSQL text cannot hold a NUL byte; reject it for every backend rather than surface a
	// driver error as a 500 (F-4e-19).
	if strings.ContainsRune(worker.Version, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "version must not contain NUL bytes")
		return
	}

	// node_id (ADR-0022 Section 4.3): ignored completely, whatever its type, unless the node routes
	// are served. When served: absent or null is NULL; a string must be a valid node ID; any other
	// type is 400.
	if h.localOnly {
		raw := strings.TrimSpace(string(req.RawNodeID))
		if raw != "" && raw != "null" {
			var id string
			if err := json.Unmarshal(req.RawNodeID, &id); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "node_id must be a string")
				return
			}
			if err := domain.ValidateNodeID(id); err != nil {
				writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid node_id")
				return
			}
			worker.NodeID = &id
		}
	}

	if err := h.coord.RegisterWorker(r.Context(), &worker); err != nil {
		if worker.NodeID != nil && errors.Is(err, domain.ErrNodeNotFound) {
			// An unregistered node is a client error on this endpoint, never a 500 or a 404.
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "node_id names no registered node")
			return
		}
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":                     "REGISTERED",
		"heartbeat_interval_seconds": 2,
	})
}

type HeartbeatRequest struct {
	AvailableSlots int `json:"available_slots"`
}

func (h *Handler) HandleWorkerHeartbeat(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("id")
	if !isValidIdentifier(workerID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}

	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}

	if err := h.coord.Heartbeat(r.Context(), workerID, req.AvailableSlots); err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ACK"})
}

func (h *Handler) HandlePollWork(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("id")
	if !isValidIdentifier(workerID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}

	assignment, err := h.coord.PollWork(r.Context(), workerID)
	if err != nil {
		if errors.Is(err, domain.ErrNoQueuedJobs) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"assignment": assignment,
	})
}

type ReportResultRequest struct {
	LeaseToken string `json:"lease_token"`
	WorkerID   string `json:"worker_id"`
	ExitCode   int    `json:"exit_code"`
	Output     string `json:"output"`
	Error      string `json:"error,omitempty"`
	// ErrorClassification is PERMANENT, TRANSIENT or UNKNOWN, supplied only with a failure
	// (protocol 6.2). The coordinator validates it; LEASE_EXPIRED is never accepted from a worker.
	ErrorClassification string `json:"error_classification,omitempty"`
}

func (h *Handler) HandleReportResult(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	attemptID := r.PathValue("attempt_id")
	if !isValidIdentifier(jobID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid job id")
		return
	}
	if !isValidIdentifier(attemptID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid attempt id")
		return
	}

	var req ReportResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}

	if !isValidIdentifier(req.WorkerID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}
	if strings.ContainsRune(req.Output, 0) || strings.ContainsRune(req.Error, 0) || strings.ContainsRune(req.ErrorClassification, 0) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "output and error must not contain NUL bytes")
		return
	}
	// attempts.exit_code is a 32-bit column; a wider value would surface as an unmapped driver
	// error and a 500 (F-4e-16).
	if req.ExitCode < math.MinInt32 || req.ExitCode > math.MaxInt32 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "exit_code out of range")
		return
	}

	job, err := h.coord.CommitResult(
		r.Context(),
		jobID,
		attemptID,
		req.LeaseToken,
		req.WorkerID,
		req.ExitCode,
		req.Output,
		req.Error,
		req.ErrorClassification,
	)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":    "COMMITTED",
		"job_state": job.State,
	})
}

// RenewLeaseRequest defines the JSON schema for POST /v1/jobs/{id}/attempts/{attempt_id}/renew.
type RenewLeaseRequest struct {
	LeaseToken string `json:"lease_token"`
	WorkerID   string `json:"worker_id"`
}

// HandleRenewLease extends an attempt lease and delivers cancellation (docs/protocol.md 6.1).
// Identifier validation and the 1 MiB body limit match the result endpoint; error statuses come
// from mapDomainErrorToHTTP so renew and result share one table (protocol 6.3). The lease token is
// compared in constant time by the repository and is never logged.
func (h *Handler) HandleRenewLease(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	attemptID := r.PathValue("attempt_id")
	if !isValidIdentifier(jobID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid job id")
		return
	}
	if !isValidIdentifier(attemptID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid attempt id")
		return
	}

	var req RenewLeaseRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}
	if !isValidIdentifier(req.WorkerID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}

	renewal, err := h.coord.RenewLease(r.Context(), jobID, attemptID, req.LeaseToken, req.WorkerID)
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "RENEWED",
		"lease_expires_at": renewal.LeaseExpiresAt.UTC(),
		"cancel_requested": renewal.CancelRequested,
	})
}

func (h *Handler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := h.coord.Ping(r.Context()); err != nil {
		slog.Error("readiness probe failed", logsafe.Err(err))
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "service temporarily unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("recovered from panic in http handler", "err", rec)
				writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func maxBytesMiddleware(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			next.ServeHTTP(w, r)
		})
	}
}

// requestDeadlineMiddleware gives every request a context deadline so repository calls cannot
// wait forever on a database connection that accepts bytes and never answers (F-4e-2).
func requestDeadlineMiddleware(d time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
