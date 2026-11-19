package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// API is a thin client for the MiniCloud HTTP API. now and sleep are injected so tests never
// use the real clock.
type API struct {
	Base  string
	HTTP  *http.Client
	Now   func() time.Time
	Sleep func(time.Duration)
}

// NewAPI returns a client bound to the real clock.
func NewAPI(base string) *API {
	return &API{Base: base, HTTP: &http.Client{Timeout: 30 * time.Second}, Now: time.Now, Sleep: time.Sleep}
}

func (a *API) do(method, path string, body any) (int, []byte, time.Duration, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, a.Base+path, rd)
	if err != nil {
		return 0, nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	t0 := a.Now()
	resp, err := a.HTTP.Do(req)
	if err != nil {
		return 0, nil, 0, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, data, a.Now().Sub(t0), nil
}

// Submit posts a job and returns its id and the request latency.
func (a *API) Submit(task string, payload any) (string, time.Duration, error) {
	code, data, d, err := a.do("POST", "/v1/jobs", map[string]any{"task_type": task, "payload": payload})
	if err != nil {
		return "", d, err
	}
	if code != http.StatusCreated {
		return "", d, fmt.Errorf("submit: HTTP %d", code)
	}
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &v); err != nil || v.ID == "" {
		return "", d, fmt.Errorf("submit: bad response")
	}
	return v.ID, d, nil
}

// JobState returns the state of a job.
func (a *API) JobState(id string) (string, error) {
	code, data, _, err := a.do("GET", "/v1/jobs/"+id, nil)
	if err != nil {
		return "", err
	}
	if code != http.StatusOK {
		return "", fmt.Errorf("get job: HTTP %d", code)
	}
	var v struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return "", err
	}
	return v.State, nil
}

// AttemptInfo is the subset of the attempts API the harness uses.
type AttemptInfo struct {
	WorkerID   string     `json:"worker_id"`
	State      string     `json:"state"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Attempts returns the attempts of a job and the raw response body.
func (a *API) Attempts(id string) ([]AttemptInfo, []byte, error) {
	code, data, _, err := a.do("GET", "/v1/jobs/"+id+"/attempts", nil)
	if err != nil {
		return nil, nil, err
	}
	if code != http.StatusOK {
		return nil, data, fmt.Errorf("attempts: HTTP %d", code)
	}
	var v struct {
		Attempts []AttemptInfo `json:"attempts"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, data, err
	}
	return v.Attempts, data, nil
}

// Register registers a probe worker.
func (a *API) Register(id string, slots int) error {
	code, _, _, err := a.do("POST", "/v1/workers/register", map[string]any{
		"id": id, "version": "mcbench", "total_slots": slots, "available_slots": slots,
		"supported_tasks": []string{"echo", "sleep", "sha256_loop"}, "executes_concurrently": true,
	})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("register: HTTP %d", code)
	}
	return nil
}

// Assignment is a claimed job.
type Assignment struct {
	JobID      string `json:"job_id"`
	AttemptID  string `json:"attempt_id"`
	LeaseToken string `json:"lease_token"`
}

// Poll calls PollWork once. It returns the assignment (nil on 204), the HTTP status and the latency.
func (a *API) Poll(workerID string) (*Assignment, int, time.Duration, error) {
	code, data, d, err := a.do("POST", "/v1/workers/"+workerID+"/poll", nil)
	if err != nil {
		return nil, 0, d, err
	}
	switch code {
	case http.StatusNoContent:
		return nil, code, d, nil
	case http.StatusOK:
		var v struct {
			Assignment Assignment `json:"assignment"`
		}
		if err := json.Unmarshal(data, &v); err != nil {
			return nil, code, d, err
		}
		return &v.Assignment, code, d, nil
	}
	return nil, code, d, fmt.Errorf("poll: HTTP %d", code)
}

// Heartbeat keeps a probe worker healthy.
func (a *API) Heartbeat(workerID string) error {
	code, _, _, err := a.do("POST", "/v1/workers/"+workerID+"/heartbeat", map[string]any{"available_slots": 8})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("heartbeat: HTTP %d", code)
	}
	return nil
}

// Report finishes a claimed attempt successfully.
func (a *API) Report(workerID string, as *Assignment) error {
	code, _, _, err := a.do("POST", "/v1/jobs/"+as.JobID+"/attempts/"+as.AttemptID+"/result",
		map[string]any{"lease_token": as.LeaseToken, "worker_id": workerID, "exit_code": 0, "output": "", "error": ""})
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return fmt.Errorf("report: HTTP %d", code)
	}
	return nil
}

// WorkerCount counts registered workers whose id starts with prefix.
func (a *API) WorkerCount(prefix string) (int, error) {
	code, data, _, err := a.do("GET", "/v1/workers", nil)
	if err != nil {
		return 0, err
	}
	if code != http.StatusOK {
		return 0, fmt.Errorf("list workers: HTTP %d", code)
	}
	var v struct {
		Workers []struct {
			ID string `json:"id"`
		} `json:"workers"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, err
	}
	n := 0
	for _, w := range v.Workers {
		if len(w.ID) >= len(prefix) && w.ID[:len(prefix)] == prefix {
			n++
		}
	}
	return n, nil
}

func isTerminal(s string) bool {
	switch s {
	case "SUCCEEDED", "FAILED", "CANCELLED", "REJECTED":
		return true
	}
	return false
}

// WaitAll polls every job until all are terminal and returns the time the last one was seen
// terminal plus the highest number seen RUNNING in one sweep (a lower bound of the true peak).
func (a *API) WaitAll(ids []string, deadline time.Duration) (last time.Time, maxRunning int, err error) {
	start := a.Now()
	pending := append([]string(nil), ids...)
	for len(pending) > 0 {
		var next []string
		running := 0
		for _, id := range pending {
			s, e := a.JobState(id)
			if e != nil {
				return last, maxRunning, e
			}
			if isTerminal(s) {
				last = a.Now()
				if s != "SUCCEEDED" {
					return last, maxRunning, fmt.Errorf("job %s ended %s", id, s)
				}
				continue
			}
			if s == "RUNNING" {
				running++
			}
			next = append(next, id)
		}
		if running > maxRunning {
			maxRunning = running
		}
		pending = next
		if len(pending) > 0 {
			if a.Now().Sub(start) > deadline {
				return last, maxRunning, fmt.Errorf("timeout waiting for %d jobs", len(pending))
			}
			a.Sleep(5 * time.Millisecond)
		}
	}
	return last, maxRunning, nil
}
