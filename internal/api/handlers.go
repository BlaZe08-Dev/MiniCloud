package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

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
	writeJSON(w, status, ErrorResponse{
		Error: ErrorDetail{
			Code:    code,
			Message: message,
		},
	})
}

func mapDomainErrorToHTTP(err error) (status int, code string) {
	switch {
	case errors.Is(err, domain.ErrJobNotFound):
		return http.StatusNotFound, "JOB_NOT_FOUND"
	case errors.Is(err, domain.ErrAttemptNotFound):
		return http.StatusNotFound, "ATTEMPT_NOT_FOUND"
	case errors.Is(err, domain.ErrWorkerNotFound):
		return http.StatusNotFound, "WORKER_NOT_FOUND"
	case errors.Is(err, domain.ErrWorkerMismatch):
		return http.StatusForbidden, "WORKER_MISMATCH"
	case errors.Is(err, domain.ErrStaleAttempt):
		return http.StatusConflict, "STALE_ATTEMPT_REJECTED"
	case errors.Is(err, domain.ErrJobAlreadyTerminal):
		return http.StatusConflict, "JOB_ALREADY_TERMINAL"
	case errors.Is(err, domain.ErrUnknownTaskType):
		return http.StatusBadRequest, "UNKNOWN_TASK_TYPE"
	case errors.Is(err, domain.ErrPayloadTooLarge):
		return http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE"
	case errors.Is(err, domain.ErrNoQueuedJobs):
		return http.StatusNoContent, "NO_QUEUED_JOBS"
	case errors.Is(err, domain.ErrNoWorkersAvailable):
		return http.StatusServiceUnavailable, "NO_WORKERS_AVAILABLE"
	case errors.Is(err, domain.ErrWorkerUnhealthy):
		return http.StatusServiceUnavailable, "WORKER_UNHEALTHY"
	default:
		var invErr *domain.InvalidStateTransitionError
		if errors.As(err, &invErr) {
			return http.StatusBadRequest, "INVALID_STATE_TRANSITION"
		}
		return http.StatusBadRequest, "INVALID_REQUEST"
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
type SubmitJobRequest struct {
	TaskType       string          `json:"task_type"`
	Payload        json.RawMessage `json:"payload"`
	TimeoutSeconds int             `json:"timeout_seconds"`
	MaxAttempts    int             `json:"max_attempts"`
}

func (h *Handler) HandleSubmitJob(w http.ResponseWriter, r *http.Request) {
	var req SubmitJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	if req.TaskType == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "task_type is required")
		return
	}

	job, err := h.coord.SubmitJob(r.Context(), req.TaskType, req.Payload, req.TimeoutSeconds, req.MaxAttempts)
	if err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, job)
}

func (h *Handler) HandleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "job id required")
		return
	}

	job, err := h.coord.GetJob(r.Context(), jobID)
	if err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) HandleListAttempts(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "job id required")
		return
	}

	attempts, err := h.coord.ListAttempts(r.Context(), jobID)
	if err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"job_id":   jobID,
		"attempts": attempts,
	})
}

func (h *Handler) HandleCancelJob(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("id")
	if jobID == "" {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "job id required")
		return
	}

	job, err := h.coord.CancelJob(r.Context(), jobID)
	if err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
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
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"workers": workers,
	})
}

func (h *Handler) HandleRegisterWorker(w http.ResponseWriter, r *http.Request) {
	var worker domain.Worker
	if err := json.NewDecoder(r.Body).Decode(&worker); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	if err := h.coord.RegisterWorker(r.Context(), &worker); err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
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
	var req HeartbeatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
		return
	}

	if err := h.coord.Heartbeat(r.Context(), workerID, req.AvailableSlots); err != nil {
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ACK"})
}

func (h *Handler) HandlePollWork(w http.ResponseWriter, r *http.Request) {
	workerID := r.PathValue("id")
	assignment, err := h.coord.PollWork(r.Context(), workerID)
	if err != nil {
		if errors.Is(err, domain.ErrNoQueuedJobs) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
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

	var req ReportResultRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", err.Error())
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
		status, code := mapDomainErrorToHTTP(err)
		writeError(w, status, code, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("recovered from panic in http handler", "err", rec)
				writeError(w, http.StatusInternalServerError, "INTERNAL_SERVER_ERROR", "an unexpected error occurred")
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
