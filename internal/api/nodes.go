package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// writeNodeError maps a node error to the shared envelope and adds Retry-After (whole seconds, from
// domain.NodeRateLimitedError) to a 429 RATE_LIMITED. NODE_LIMIT_REACHED carries no Retry-After.
func writeNodeError(w http.ResponseWriter, err error) {
	status, code, msg := mapDomainErrorToHTTP(err)
	if code == "RATE_LIMITED" {
		var rl *domain.NodeRateLimitedError
		if errors.As(err, &rl) {
			w.Header().Set("Retry-After", strconv.Itoa(rl.RetryAfterSeconds()))
		}
	}
	writeError(w, status, code, msg)
}

// HandleRegisterNode serves POST /v1/nodes/register (ADR-0022 Section 4.1).
func (h *Handler) HandleRegisterNode(w http.ResponseWriter, r *http.Request) {
	var reg domain.NodeRegistration
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}
	// Validate the ID before any repository call (audit A-05b).
	if err := domain.ValidateNodeID(reg.NodeID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid node_id")
		return
	}
	res, err := h.coord.RegisterNode(r.Context(), reg)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "REGISTERED",
		"node_id":          reg.NodeID,
		"hardware_version": res.HardwareVersion,
	})
}

// HandleNodeTelemetry serves POST /v1/nodes/{node_id}/telemetry (ADR-0022 Section 4.2).
func (h *Handler) HandleNodeTelemetry(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if err := domain.ValidateNodeID(nodeID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid node id")
		return
	}
	var report domain.NodeTelemetryReport
	if err := json.NewDecoder(r.Body).Decode(&report); err != nil {
		writeError(w, http.StatusBadRequest, "MALFORMED_JSON", "malformed JSON request body")
		return
	}
	if err := h.coord.RecordNodeTelemetry(r.Context(), nodeID, report); err != nil {
		writeNodeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ACK"})
}

// nodeSummary is one row of GET /v1/nodes: no hardware detail beyond os and arch, and no telemetry
// value (ADR-0022 Section 4.4). Unknown values are null.
type nodeSummary struct {
	NodeID          string  `json:"node_id"`
	DisplayName     string  `json:"display_name"`
	OS              *string `json:"os"`
	Arch            *string `json:"arch"`
	HardwareVersion *int    `json:"hardware_version"`
	WorkerCount     int     `json:"worker_count"`
	TelemetryAgeMS  *int64  `json:"telemetry_age_ms"`
}

func summarize(v *domain.NodeView, now time.Time) nodeSummary {
	s := nodeSummary{NodeID: v.ID, DisplayName: v.DisplayName, WorkerCount: len(v.WorkerIDs)}
	if v.Hardware != nil {
		os, arch, ver := v.Hardware.OS, v.Hardware.Arch, v.Hardware.Version
		s.OS, s.Arch, s.HardwareVersion = &os, &arch, &ver
	}
	if v.Telemetry != nil {
		// Display only: coordinator clock minus received_at (V2-1c decision, not in the ADR).
		age := now.Sub(v.Telemetry.ReceivedAt).Milliseconds()
		if age < 0 {
			age = 0
		}
		s.TelemetryAgeMS = &age
	}
	return s
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// HandleListNodes serves GET /v1/nodes?limit=&after= (summary only, paged by node ID).
func (h *Handler) HandleListNodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := domain.DefaultNodePageSize
	if q.Has("limit") {
		raw := q.Get("limit")
		n, err := strconv.Atoi(raw)
		if !allDigits(raw) || err != nil || n < 1 || n > domain.MaxNodePageSize {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "limit must be between 1 and "+strconv.Itoa(domain.MaxNodePageSize))
			return
		}
		limit = n
	}
	after := ""
	if q.Has("after") {
		after = q.Get("after")
		if err := domain.ValidateNodeID(after); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "after must be a canonical node id")
			return
		}
	}
	views, err := h.coord.ListNodes(r.Context(), after, limit)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	now := h.coord.NodeNow()
	out := make([]nodeSummary, 0, len(views))
	for _, v := range views {
		out = append(out, summarize(v, now))
	}
	var next *string
	if len(views) == limit {
		last := views[len(views)-1].ID
		next = &last
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"nodes":       out,
		"next_cursor": next,
	})
}

// HandleGetNode serves GET /v1/nodes/{node_id}: the full view, the only place the sensitive fields
// appear. The view carries no session_id and no seq.
func (h *Handler) HandleGetNode(w http.ResponseWriter, r *http.Request) {
	nodeID := r.PathValue("node_id")
	if err := domain.ValidateNodeID(nodeID); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid node id")
		return
	}
	view, err := h.coord.GetNode(r.Context(), nodeID)
	if err != nil {
		writeNodeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}
