package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
)

type trackingReadCloser struct {
	io.ReadCloser
	onRead func(n int)
}

func (t *trackingReadCloser) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 && t.onRead != nil {
		t.onRead(n)
	}
	return n, err
}

type countingTransport struct {
	base      http.RoundTripper
	bytesRead atomic.Int64
}

func (ct *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := ct.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	resp.Body = &trackingReadCloser{
		ReadCloser: resp.Body,
		onRead: func(n int) {
			ct.bytesRead.Add(int64(n))
		},
	}
	return resp, nil
}

// TestPollWork_OversizedResponseRejected guards SEC-060:
// Fake coordinator streams a body larger than MaxPollResponseBytes.
// pollWork must return an error and read at most MaxPollResponseBytes (plus small slack).
func TestPollWork_OversizedResponseRejected(t *testing.T) {
	const slack = 1024

	t.Run("AssignmentDecode", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			// Stream a large assignment body with payload exceeding 2 * MaxPollResponseBytes.
			// It starts as valid JSON but cannot complete within the limit.
			prefix := `{"assignment":{"job_id":"job-over","attempt_id":"att-over","task_type":"echo","payload":"`
			w.Write([]byte(prefix))
			padding := strings.Repeat("A", 2*MaxPollResponseBytes)
			w.Write([]byte(padding))
			w.Write([]byte(`"}}`))
		}))
		defer ts.Close()

		cfg := &config.WorkerConfig{
			WorkerID:       "w-test",
			CoordinatorURL: ts.URL,
			TotalSlots:     1,
		}
		agent := NewAgent(cfg, executor.NewRegistry())
		ct := &countingTransport{base: ts.Client().Transport}
		if ct.base == nil {
			ct.base = http.DefaultTransport
		}
		agent.httpClient = &http.Client{Transport: ct}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		asg, err := agent.pollWork(ctx)
		if err == nil {
			t.Fatalf("expected error from oversized response, got nil assignment: %+v", asg)
		}
		if asg != nil {
			t.Fatalf("expected nil assignment on error, got: %+v", asg)
		}

		bytesRead := ct.bytesRead.Load()
		if bytesRead > int64(MaxPollResponseBytes+slack) {
			t.Fatalf("pollWork read %d bytes; want at most limit %d bytes (limit %d + slack %d)",
				bytesRead, MaxPollResponseBytes+slack, MaxPollResponseBytes, slack)
		}
	})

	t.Run("ErrorEnvelopeDecode", func(t *testing.T) {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			// Stream an error envelope larger than 2 * MaxPollResponseBytes.
			prefix := `{"error":{"code":"SERVICE_UNAVAILABLE","message":"`
			w.Write([]byte(prefix))
			padding := strings.Repeat("E", 2*MaxPollResponseBytes)
			w.Write([]byte(padding))
			w.Write([]byte(`"}}`))
		}))
		defer ts.Close()

		cfg := &config.WorkerConfig{
			WorkerID:       "w-test",
			CoordinatorURL: ts.URL,
			TotalSlots:     1,
		}
		agent := NewAgent(cfg, executor.NewRegistry())
		ct := &countingTransport{base: ts.Client().Transport}
		if ct.base == nil {
			ct.base = http.DefaultTransport
		}
		agent.httpClient = &http.Client{Transport: ct}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		asg, err := agent.pollWork(ctx)
		if err == nil {
			t.Fatalf("expected error from 503 response, got nil")
		}
		if asg != nil {
			t.Fatalf("expected nil assignment on error, got: %+v", asg)
		}

		bytesRead := ct.bytesRead.Load()
		if bytesRead > int64(MaxPollResponseBytes+slack) {
			t.Fatalf("pollWork 503 read %d bytes; want at most limit %d bytes (limit %d + slack %d)",
				bytesRead, MaxPollResponseBytes+slack, MaxPollResponseBytes, slack)
		}
	})
}

// TestPollWork_MaxLegalAssignmentAccepted guards SEC-060:
// An assignment at the largest legal payload size (executor.MaxPayloadSizeBytes = 64 KiB)
// still decodes cleanly.
func TestPollWork_MaxLegalAssignmentAccepted(t *testing.T) {
	// Construct a valid JSON string of exact length executor.MaxPayloadSizeBytes.
	rawJSON := make([]byte, executor.MaxPayloadSizeBytes)
	rawJSON[0] = '"'
	for i := 1; i < executor.MaxPayloadSizeBytes-1; i++ {
		rawJSON[i] = 'a'
	}
	rawJSON[executor.MaxPayloadSizeBytes-1] = '"'

	legalAsg := &coordinator.WorkAssignment{
		JobID:                "job-legal-max",
		AttemptID:            "att-legal-max",
		TaskType:             "echo",
		Payload:              json.RawMessage(rawJSON),
		TimeoutSeconds:       30,
		LeaseDurationSeconds: 10,
		LeaseToken:           "token-legal-max",
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"assignment": legalAsg,
		})
	}))
	defer ts.Close()

	cfg := &config.WorkerConfig{
		WorkerID:       "w-test",
		CoordinatorURL: ts.URL,
		TotalSlots:     1,
	}
	agent := NewAgent(cfg, executor.NewRegistry())
	agent.httpClient = ts.Client()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	asg, err := agent.pollWork(ctx)
	if err != nil {
		t.Fatalf("expected successful decode of max legal assignment, got err: %v", err)
	}
	if asg == nil {
		t.Fatalf("expected non-nil assignment, got nil")
	}
	if asg.JobID != legalAsg.JobID {
		t.Errorf("JobID = %q, want %q", asg.JobID, legalAsg.JobID)
	}
	if asg.AttemptID != legalAsg.AttemptID {
		t.Errorf("AttemptID = %q, want %q", asg.AttemptID, legalAsg.AttemptID)
	}
	if len(asg.Payload) != executor.MaxPayloadSizeBytes {
		t.Errorf("Payload length = %d, want %d", len(asg.Payload), executor.MaxPayloadSizeBytes)
	}
	if !bytes.Equal(asg.Payload, rawJSON) {
		t.Errorf("Payload content mismatch")
	}
}

// TestWorker_URLPathEscaping_RenewAndReport guards SEC-061:
// IDs containing "../", "?", "#", and "%2f" reach the fake coordinator
// as one escaped path segment each in both renewOnce and reportResult.
func TestWorker_URLPathEscaping_RenewAndReport(t *testing.T) {
	testCases := []struct {
		name      string
		jobID     string
		attemptID string
	}{
		{name: "dotdot_slash", jobID: "../", attemptID: "att-1"},
		{name: "question_mark", jobID: "job-1", attemptID: "?"},
		{name: "hash_fragment", jobID: "#", attemptID: "att-2"},
		{name: "percent_encoded_slash", jobID: "job-2", attemptID: "%2f"},
	}

	t.Run("RenewOnce", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				var recordedPath string
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					recordedPath = r.URL.EscapedPath()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{"cancel_requested": false}`))
				}))
				defer ts.Close()

				cfg := &config.WorkerConfig{
					WorkerID:       "w-test",
					CoordinatorURL: ts.URL,
					TotalSlots:     1,
				}
				agent := NewAgent(cfg, executor.NewRegistry())
				agent.httpClient = ts.Client()

				asg := &coordinator.WorkAssignment{
					JobID:      tc.jobID,
					AttemptID:  tc.attemptID,
					LeaseToken: "tok-test",
				}

				agent.renewOnce(context.Background(), asg, 5*time.Second)

				wantPath := fmt.Sprintf("/v1/jobs/%s/attempts/%s/renew",
					url.PathEscape(tc.jobID), url.PathEscape(tc.attemptID))
				if recordedPath != wantPath {
					t.Fatalf("renewOnce recorded path = %q, want %q", recordedPath, wantPath)
				}
			})
		}
	})

	t.Run("ReportResult", func(t *testing.T) {
		for _, tc := range testCases {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				var recordedPath string
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					recordedPath = r.URL.EscapedPath()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusOK)
					w.Write([]byte(`{}`))
				}))
				defer ts.Close()

				cfg := &config.WorkerConfig{
					WorkerID:       "w-test",
					CoordinatorURL: ts.URL,
					TotalSlots:     1,
				}
				agent := NewAgent(cfg, executor.NewRegistry())
				agent.httpClient = ts.Client()

				_, _, _ = agent.reportResult(context.Background(), tc.jobID, tc.attemptID, "tok-test", 0, "ok", "", domain.ErrorClass(""))

				wantPath := fmt.Sprintf("/v1/jobs/%s/attempts/%s/result",
					url.PathEscape(tc.jobID), url.PathEscape(tc.attemptID))
				if recordedPath != wantPath {
					t.Fatalf("reportResult recorded path = %q, want %q", recordedPath, wantPath)
				}
			})
		}
	})
}
