package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	uriPasswordRegex   = regexp.MustCompile(`://([^:]+):([^@]+)@`)
	paramPasswordRegex = regexp.MustCompile(`(?i)(password|passwd|pwd)\s*=\s*[^\s]+`)
)

func sanitizeError(msg string) string {
	msg = uriPasswordRegex.ReplaceAllString(msg, `://$1:***@`)
	msg = paramPasswordRegex.ReplaceAllString(msg, `$1=***`)
	return msg
}

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
		// PostgreSQL Class 08 — Connection Exception or Class 57 — Operator Intervention
		if strings.HasPrefix(pgErr.Code, "08") || pgErr.Code == "57P01" || pgErr.Code == "57P02" || pgErr.Code == "57P03" {
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

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
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
		slog.Error("database outage or connection failure", "err", sanitizeError(err.Error()))
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
	case errors.Is(err, domain.ErrStaleAttempt):
		return http.StatusConflict, "STALE_ATTEMPT_REJECTED", err.Error()
	case errors.Is(err, domain.ErrJobAlreadyTerminal):
		return http.StatusConflict, "JOB_ALREADY_TERMINAL", err.Error()
	case errors.Is(err, domain.ErrUnknownTaskType):
		return http.StatusBadRequest, "UNKNOWN_TASK_TYPE", err.Error()
	case errors.Is(err, domain.ErrPayloadTooLarge):
		return http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", err.Error()
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
	default:
		var invErr *domain.InvalidStateTransitionError
		if errors.As(err, &invErr) {
			return http.StatusBadRequest, "INVALID_STATE_TRANSITION", invErr.Error()
		}
		slog.Error("internal server error", "err", sanitizeError(err.Error()))
		return http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error"
	}
}

// Handler holds dependencies for all API endpoints.
type Handler struct {
	coord *coordinator.Service
}

func NewHandler(coord *coordinator.Service) *Handler {
	return &Handler{coord: coord}
}

// SubmitJobRequest defines the JSON schema for POST /v1/jobs.
// If max_attempts is omitted or null, it defaults to 3 (valid range: 1..20).
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

	maxAttempts := coordinator.DefaultMaxAttempts
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

func (h *Handler) HandleListWorkers(w http.ResponseWriter, r *http.Request) {
	workers, err := h.coord.ListWorkers(r.Context())
	if err != nil {
		status, code, msg := mapDomainErrorToHTTP(err)
		writeError(w, status, code, msg)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workers": workers,
	})
}

func (h *Handler) HandleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	var worker domain.Worker
	if err := json.NewDecoder(r.Body).Decode(&worker); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}

	if !isValidIdentifier(worker.ID) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid worker id")
		return
	}

	if err := h.coord.RegisterWorker(r.Context(), &worker); err != nil {
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

	job, err := h.coord.CommitResult(
		r.Context(),
		jobID,
		attemptID,
		req.LeaseToken,
		req.WorkerID,
		req.ExitCode,
		req.Output,
		req.Error,
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

func (h *Handler) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := h.coord.Ping(r.Context()); err != nil {
		slog.Error("readiness probe failed", "err", sanitizeError(err.Error()))
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
