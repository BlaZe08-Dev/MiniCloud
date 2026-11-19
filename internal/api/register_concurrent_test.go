package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// TestAPI_RegisterWorker_ExecutesConcurrentlyOptional: ADR-0020 Section 2.6, executes_concurrently
// is an optional, additive register field. Absent stores false (the v1.0.0 and V2-0 workers), true
// stores true, a re-registration without it resets it, the register response is unchanged, and
// GET /v1/workers shows the stored value.
func TestAPI_RegisterWorker_ExecutesConcurrentlyOptional(t *testing.T) {
	ts, _ := setupTestServer()
	defer ts.Close()

	register := func(body map[string]any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		resp, err := http.Post(ts.URL+"/v1/workers/register", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("register: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("register status %d, body %s", resp.StatusCode, b)
		}
		var out map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode register response: %v", err)
		}
		return out
	}
	listed := func(id string) any {
		t.Helper()
		resp, err := http.Get(ts.URL + "/v1/workers")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		defer resp.Body.Close()
		var out struct {
			Workers []map[string]any `json:"workers"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		for _, w := range out.Workers {
			if w["id"] == id {
				return w["executes_concurrently"]
			}
		}
		t.Fatalf("worker %s not listed", id)
		return nil
	}
	base := func(id string) map[string]any {
		return map[string]any{"id": id, "version": "v0.1.0", "total_slots": 4, "supported_tasks": []string{"echo"}}
	}

	old := register(base("w-old"))
	if len(old) != 2 || old["status"] != "REGISTERED" {
		t.Fatalf("register response changed: %v", old)
	}
	if got := listed("w-old"); got != false {
		t.Fatalf("absent field: listed executes_concurrently = %#v, want false", got)
	}

	b := base("w-new")
	b["executes_concurrently"] = true
	if resp := register(b); len(resp) != 2 || resp["status"] != "REGISTERED" {
		t.Fatalf("register response with the field changed: %v", resp)
	}
	if got := listed("w-new"); got != true {
		t.Fatalf("declared: listed executes_concurrently = %#v, want true", got)
	}

	register(base("w-new"))
	if got := listed("w-new"); got != false {
		t.Fatalf("re-registered without the field: listed executes_concurrently = %#v, want false", got)
	}
}
