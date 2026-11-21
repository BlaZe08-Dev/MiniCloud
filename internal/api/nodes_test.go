package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// nodeSpyRepo counts the repository calls that must never be reached with a bad node ID.
type nodeSpyRepo struct {
	storage.Repository
	calls atomic.Int64
}

func (r *nodeSpyRepo) RegisterNode(ctx context.Context, reg domain.NodeRegistration, d time.Duration) (*domain.NodeRegistrationResult, error) {
	r.calls.Add(1)
	return r.Repository.RegisterNode(ctx, reg, d)
}
func (r *nodeSpyRepo) RecordNodeTelemetry(ctx context.Context, id string, rep domain.NodeTelemetryReport) error {
	r.calls.Add(1)
	return r.Repository.RecordNodeTelemetry(ctx, id, rep)
}
func (r *nodeSpyRepo) GetNode(ctx context.Context, id string) (*domain.NodeView, error) {
	r.calls.Add(1)
	return r.Repository.GetNode(ctx, id)
}
func (r *nodeSpyRepo) ListNodes(ctx context.Context, after string, limit int) ([]*domain.NodeView, error) {
	r.calls.Add(1)
	return r.Repository.ListNodes(ctx, after, limit)
}
func (r *nodeSpyRepo) CountNodes(ctx context.Context) (int, error) {
	r.calls.Add(1)
	return r.Repository.CountNodes(ctx)
}
func (r *nodeSpyRepo) UpsertWorker(ctx context.Context, w *domain.Worker) error {
	r.calls.Add(1)
	return r.Repository.UpsertWorker(ctx, w)
}

type nodeEnv struct {
	ts   *httptest.Server
	repo *nodeSpyRepo
	svc  *coordinator.Service
	clk  *apiTestClock
}

func newNodeEnv(t *testing.T, localOnly bool) *nodeEnv {
	t.Helper()
	clk := &apiTestClock{now: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}
	repo := &nodeSpyRepo{Repository: storage.NewInMemoryRepository(storage.WithClock(clk))}
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetClock(clk)
	ts := httptest.NewServer(api.NewServer("127.0.0.1:0", svc, api.WithLocalOnly(localOnly)).Handler())
	t.Cleanup(ts.Close)
	return &nodeEnv{ts: ts, repo: repo, svc: svc, clk: clk}
}

func (e *nodeEnv) do(t *testing.T, method, path, body string, hdr map[string]string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(method, e.ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		if strings.EqualFold(k, "Host") {
			req.Host = v
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(b)
}

func (e *nodeEnv) post(t *testing.T, path, body string) (int, http.Header, string) {
	return e.do(t, http.MethodPost, path, body, nil)
}
func (e *nodeEnv) get(t *testing.T, path string) (int, http.Header, string) {
	return e.do(t, http.MethodGet, path, "", nil)
}

func nid(n int) string { return fmt.Sprintf("%08x-0000-4000-8000-%012x", 0xabc00000+n, n) }

func nodeBody(id string, mem int64) string {
	return fmt.Sprintf(`{"node_id":%q,"display_name":"desk","contract_version":1,"agent_version":"agent-secret-9","hardware":{"os":"linux","arch":"amd64","cpu_model":"Secret CPU 9000","cpu_logical_cores":8,"cpu_physical_cores":4,"memory_total_bytes":%d,"disk_total_bytes":null,"gpu_count":null,"gpu_model":null,"gpu_vram_total_bytes":null}}`, id, mem)
}

func telBody(session string, seq int64, age int64) string {
	return fmt.Sprintf(`{"contract_version":1,"session_id":%q,"seq":%d,"sample_age_ms":%d,"cpu_load_1m_milli":851,"memory_available_bytes":9126805504,"disk_free_bytes":null}`, session, seq, age)
}

func codeOf(t *testing.T, body string) string {
	t.Helper()
	var e struct {
		Error struct{ Code string } `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("not an error envelope: %q", body)
	}
	return e.Error.Code
}

func (e *nodeEnv) mustRegisterNode(t *testing.T, id string, mem int64) {
	t.Helper()
	if st, _, b := e.post(t, "/v1/nodes/register", nodeBody(id, mem)); st != 200 {
		t.Fatalf("register node: %d %s", st, b)
	}
}

func workerBody(id, nodeIDJSON string) string {
	extra := ""
	if nodeIDJSON != "" {
		extra = `,"node_id":` + nodeIDJSON
	}
	return fmt.Sprintf(`{"id":%q,"version":"v","total_slots":2,"supported_tasks":["echo"]%s}`, id, extra)
}

func TestAPI_RegisterNode_Validation(t *testing.T) {
	e := newNodeEnv(t, true)
	id := nid(1)
	bad := []struct {
		name, body string
		status     int
		code       string
	}{
		{"malformed json", `{"node_id":`, 400, "MALFORMED_JSON"},
		{"empty body", ``, 400, "MALFORMED_JSON"},
		{"missing node id", strings.Replace(nodeBody(id, 1), id, "", 1), 400, "INVALID_REQUEST"},
		{"uppercase uuid", strings.Replace(nodeBody(id, 1), "abc00001", "ABC00001", 1), 400, "INVALID_REQUEST"},
		{"uuid v1", strings.Replace(nodeBody(id, 1), "-4000-", "-1000-", 1), 400, "INVALID_REQUEST"},
		{"nul in id", strings.Replace(nodeBody(id, 1), id, `abc00001-0000-4000-8000-0000000000\u0000`, 1), 400, "INVALID_REQUEST"},
		{"contract 2", strings.Replace(nodeBody(id, 1), `"contract_version":1`, `"contract_version":2`, 1), 400, "UNSUPPORTED_CONTRACT_VERSION"},
		{"contract missing", strings.Replace(nodeBody(id, 1), `"contract_version":1,`, ``, 1), 400, "UNSUPPORTED_CONTRACT_VERSION"},
		{"display name control", strings.Replace(nodeBody(id, 1), `"desk"`, `"a\tb"`, 1), 400, "INVALID_REQUEST"},
		{"display name too long", strings.Replace(nodeBody(id, 1), `"desk"`, `"`+strings.Repeat("x", 65)+`"`, 1), 400, "INVALID_REQUEST"},
		{"agent version nul", strings.Replace(nodeBody(id, 1), `agent-secret-9`, `a\u0000b`, 1), 400, "INVALID_REQUEST"},
		{"os uppercase", strings.Replace(nodeBody(id, 1), `"os":"linux"`, `"os":"Linux"`, 1), 400, "INVALID_REQUEST"},
		{"cpu model non ascii", strings.Replace(nodeBody(id, 1), `Secret CPU 9000`, `CPU é`, 1), 400, "INVALID_REQUEST"},
		{"negative cores", strings.Replace(nodeBody(id, 1), `"cpu_logical_cores":8`, `"cpu_logical_cores":-1`, 1), 400, "INVALID_REQUEST"},
		{"logical below physical", strings.Replace(nodeBody(id, 1), `"cpu_logical_cores":8`, `"cpu_logical_cores":2`, 1), 400, "INVALID_REQUEST"},
		{"memory above 2^53-1", nodeBody(id, 1<<53), 400, "INVALID_REQUEST"},
		{"memory negative", nodeBody(id, -1), 400, "INVALID_REQUEST"},
		{"memory is a string", strings.Replace(nodeBody(id, 1), `"memory_total_bytes":1`, `"memory_total_bytes":"1"`, 1), 400, "MALFORMED_JSON"},
		{"memory fraction", strings.Replace(nodeBody(id, 1), `"memory_total_bytes":1`, `"memory_total_bytes":1.5`, 1), 400, "MALFORMED_JSON"},
		{"no hardware", `{"node_id":"` + id + `","display_name":"d","contract_version":1}`, 400, "INVALID_REQUEST"},
	}
	for _, c := range bad {
		st, _, b := e.post(t, "/v1/nodes/register", c.body)
		if st != c.status || codeOf(t, b) != c.code {
			t.Errorf("%s: got %d %s, want %d %s", c.name, st, b, c.status, c.code)
		}
	}
	if n, _ := e.repo.CountNodes(context.Background()); n != 0 {
		t.Fatalf("rejected registrations created %d nodes", n)
	}
	good := `{"unknown_field":{"x":1},` + nodeBody(id, 17179869184)[1:]
	for i := 0; i < 2; i++ {
		st, _, b := e.post(t, "/v1/nodes/register", good)
		want := fmt.Sprintf(`{"hardware_version":1,"node_id":%q,"status":"REGISTERED"}`+"\n", id)
		if st != 200 || b != want {
			t.Fatalf("good registration #%d: %d %q, want 200 %q (idempotent, unknown fields ignored)", i+1, st, b, want)
		}
	}
}

func TestAPI_NodeTelemetry_UnknownNode404(t *testing.T) {
	e := newNodeEnv(t, true)
	for _, seq := range []int64{1, 1 << 50} {
		st, _, b := e.post(t, "/v1/nodes/"+nid(7)+"/telemetry", telBody(nid(100), seq, 0))
		if st != 404 || codeOf(t, b) != "NODE_NOT_FOUND" {
			t.Fatalf("seq %d: %d %s, want 404 NODE_NOT_FOUND (never 409 or 500)", seq, st, b)
		}
	}
	if _, err := e.repo.Repository.GetNode(context.Background(), nid(7)); err == nil {
		t.Fatal("an unknown-node report created a node")
	}
	if n, _ := e.repo.Repository.CountNodes(context.Background()); n != 0 {
		t.Fatalf("nodes after unknown-node reports = %d, want 0 (nothing written)", n)
	}
}

func TestAPI_RegisterWorker_NodeIDOptional(t *testing.T) {
	type tc struct {
		name, nodeJSON string
		servedStatus   int
		servedNode     string // "" = NULL
	}
	known := nid(1)
	cases := []tc{
		{"absent", "", 200, ""},
		{"null", "null", 200, ""},
		{"valid", `"` + known + `"`, 200, known},
		{"unknown", `"` + nid(2) + `"`, 400, ""},
		{"malformed", `"not-a-uuid"`, 400, ""},
		{"empty string", `""`, 400, ""},
		{"nul byte", `"abc00001-0000-4000-8000-0000000000\u0000"`, 400, ""},
		{"number 5", `5`, 400, ""},
		{"true", `true`, 400, ""},
		{"object", `{"a":1}`, 400, ""},
		{"array", `["x"]`, 400, ""},
	}
	for _, served := range []bool{true, false} {
		for _, c := range cases {
			t.Run(fmt.Sprintf("served=%v/%s", served, c.name), func(t *testing.T) {
				e := newNodeEnv(t, served)
				// Register the known node straight in the repository so the not-served server,
				// which has no node routes, also has a registered node to ignore.
				_, err := e.repo.Repository.RegisterNode(context.Background(), domain.NodeRegistration{NodeID: known, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				st, _, b := e.post(t, "/v1/workers/register", workerBody("w1", c.nodeJSON))
				wantStatus, wantNode := c.servedStatus, c.servedNode
				if !served {
					wantStatus, wantNode = 200, ""
				}
				if st != wantStatus {
					t.Fatalf("status = %d %s, want %d", st, b, wantStatus)
				}
				if wantStatus == 400 {
					if codeOf(t, b) != "INVALID_REQUEST" {
						t.Fatalf("code = %s, want INVALID_REQUEST", b)
					}
					if _, err := e.repo.Repository.GetWorker(context.Background(), "w1"); err == nil {
						t.Fatal("a rejected registration stored a worker")
					}
					return
				}
				w, err := e.repo.Repository.GetWorker(context.Background(), "w1")
				if err != nil {
					t.Fatal(err)
				}
				got := ""
				if w.NodeID != nil {
					got = *w.NodeID
				}
				if got != wantNode {
					t.Fatalf("stored node_id = %q, want %q", got, wantNode)
				}
			})
		}
	}
}

func decodeWorkers(t *testing.T, body string) []map[string]any {
	t.Helper()
	var r struct {
		Workers []map[string]any `json:"workers"`
	}
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("workers body: %v: %s", err, body)
	}
	return r.Workers
}

func TestAPI_ListWorkers_NodeIDNullForLegacy(t *testing.T) {
	e := newNodeEnv(t, true)
	e.mustRegisterNode(t, nid(1), 100)
	e.post(t, "/v1/workers/register", workerBody("legacy", ""))
	e.post(t, "/v1/workers/register", workerBody("onnode", `"`+nid(1)+`"`))
	_, _, b := e.get(t, "/v1/workers")
	byID := map[string]map[string]any{}
	for _, w := range decodeWorkers(t, b) {
		byID[w["id"].(string)] = w
	}
	v, present := byID["legacy"]["node_id"]
	if !present || v != nil {
		t.Fatalf("legacy worker node_id present=%v value=%#v, want present and null (not omitted, not \"\"): %s", present, v, b)
	}
	if byID["onnode"]["node_id"] != nid(1) {
		t.Fatalf("worker on a node: node_id = %#v", byID["onnode"]["node_id"])
	}
}

func TestAPI_Node_SampledAtUsesDatabaseClock(t *testing.T) {
	e := newNodeEnv(t, true) // repository ("database") clock and coordinator ("client side") clock are separate values
	e.mustRegisterNode(t, nid(1), 100)
	dbNow := e.clk.Now().Add(10 * time.Second)
	e.clk.Advance(10 * time.Second) // moves the shared test clock; then give the coordinator its own, wrong clock
	wrong := &apiTestClock{now: dbNow.Add(48 * time.Hour)}
	e.svc.SetClock(wrong)
	if st, _, b := e.post(t, "/v1/nodes/"+nid(1)+"/telemetry", telBody(nid(100), 1, 4000)); st != 200 {
		t.Fatalf("telemetry: %d %s", st, b)
	}
	v, err := e.repo.Repository.GetNode(context.Background(), nid(1))
	if err != nil || v.Telemetry == nil {
		t.Fatalf("GetNode: %v %+v", err, v)
	}
	wantSampled := dbNow.Add(-4 * time.Second)
	if !v.Telemetry.ReceivedAt.Equal(dbNow) || !v.Telemetry.SampledAt.Equal(wantSampled) {
		t.Fatalf("received_at=%v sampled_at=%v, want %v and %v (repository clock minus sample_age_ms, not the coordinator or client clock)", v.Telemetry.ReceivedAt, v.Telemetry.SampledAt, dbNow, wantSampled)
	}
}

func TestAPI_Node_SampleAgeBeforeCreatedIsRejected(t *testing.T) {
	e := newNodeEnv(t, true)
	e.mustRegisterNode(t, nid(1), 100)
	e.clk.Advance(5 * time.Second)
	if st, _, b := e.post(t, "/v1/nodes/"+nid(1)+"/telemetry", telBody(nid(100), 1, 6000)); st != 400 || codeOf(t, b) != "INVALID_REQUEST" {
		t.Fatalf("age older than the node: %d %s, want 400 INVALID_REQUEST", st, b)
	}
	if st, _, b := e.post(t, "/v1/nodes/"+nid(1)+"/telemetry", telBody(nid(100), 1, 5000)); st != 200 {
		t.Fatalf("age exactly the node age: %d %s, want 200", st, b)
	}
	if st, _, b := e.post(t, "/v1/nodes/"+nid(1)+"/telemetry", telBody(nid(100), 2, 2147483648)); st != 400 {
		t.Fatalf("age above 2^31-1: %d %s, want 400", st, b)
	}
}

func TestAPI_ListNodes_SummaryOmitsSensitiveFields(t *testing.T) {
	e := newNodeEnv(t, true)
	id := nid(1)
	e.mustRegisterNode(t, id, 17179869184)
	e.clk.Advance(2 * time.Second)
	if st, _, b := e.post(t, "/v1/nodes/"+id+"/telemetry", telBody(nid(100), 5, 0)); st != 200 {
		t.Fatalf("telemetry: %d %s", st, b)
	}
	e.post(t, "/v1/workers/register", workerBody("w1", `"`+id+`"`))
	e.clk.Advance(1500 * time.Millisecond)

	sensitive := []string{"cpu_model", "gpu_model", "cpu_logical_cores", "cpu_physical_cores", "gpu_count", "memory_total_bytes",
		"disk_total_bytes", "gpu_vram_total_bytes", "agent_version", "cpu_load_1m_milli", "memory_available_bytes", "disk_free_bytes",
		"Secret CPU 9000", "agent-secret-9", "17179869184", "9126805504", "851", "session_id", "seq"}

	// Summary: exactly the seven fields.
	st, _, b := e.get(t, "/v1/nodes")
	if st != 200 {
		t.Fatalf("GET /v1/nodes: %d %s", st, b)
	}
	var list struct {
		Nodes      []map[string]any `json:"nodes"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(b), &list); err != nil || len(list.Nodes) != 1 {
		t.Fatalf("summary body: %v %s", err, b)
	}
	want := []string{"arch", "display_name", "hardware_version", "node_id", "os", "telemetry_age_ms", "worker_count"}
	var got []string
	for k := range list.Nodes[0] {
		got = append(got, k)
	}
	if len(got) != len(want) {
		t.Fatalf("summary keys = %v, want %v", got, want)
	}
	for _, k := range want {
		if _, ok := list.Nodes[0][k]; !ok {
			t.Fatalf("summary lacks %s: %s", k, b)
		}
	}
	for _, s := range sensitive {
		if strings.Contains(b, s) {
			t.Errorf("summary contains sensitive %q: %s", s, b)
		}
	}
	if list.Nodes[0]["telemetry_age_ms"].(float64) != 1500 || list.Nodes[0]["worker_count"].(float64) != 1 {
		t.Fatalf("summary values: %s", b)
	}

	// Full view: carries the sensitive fields, never session_id or seq.
	st, _, full := e.get(t, "/v1/nodes/"+id)
	if st != 200 {
		t.Fatalf("GET node: %d %s", st, full)
	}
	for _, s := range []string{"cpu_model", "Secret CPU 9000", "memory_total_bytes", "17179869184", "agent_version", "agent-secret-9", "cpu_load_1m_milli", "memory_available_bytes", "received_at", "sampled_at", "worker_ids", "w1", `"gpu_count":null`, `"disk_free_bytes":null`} {
		if !strings.Contains(full, s) {
			t.Errorf("full view lacks %q: %s", s, full)
		}
	}
	for _, s := range []string{"session_id", "seq"} {
		if strings.Contains(full, s) {
			t.Errorf("full view contains %q: %s", s, full)
		}
	}

	// /v1/workers carries node_id only; job events carry nothing from a node.
	_, _, wb := e.get(t, "/v1/workers")
	for _, s := range sensitive {
		if strings.Contains(wb, s) {
			t.Errorf("/v1/workers contains %q: %s", s, wb)
		}
	}
	job, err := e.svc.SubmitJob(context.Background(), "echo", []byte(`{"message":"hi"}`), 60, 3)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.PollWork(context.Background(), "w1"); err != nil {
		t.Logf("poll: %v", err)
	}
	events, err := e.repo.Repository.ListEventsByJob(context.Background(), job.ID)
	if err != nil || len(events) == 0 {
		t.Fatalf("events: %v %d", err, len(events))
	}
	eb, _ := json.Marshal(events)
	for _, s := range sensitive {
		if strings.Contains(string(eb), s) {
			t.Errorf("job events contain %q: %s", s, eb)
		}
	}
}

func TestAPI_RegisterNode_RateLimited_429RetryAfter(t *testing.T) {
	e := newNodeEnv(t, true)
	id := nid(1)
	e.mustRegisterNode(t, id, 100)
	e.clk.Advance(10 * time.Second)
	// An unchanged hardware body inside the interval is never limited (N14).
	if st, _, b := e.post(t, "/v1/nodes/register", nodeBody(id, 100)); st != 200 {
		t.Fatalf("unchanged hardware inside the interval: %d %s, want 200", st, b)
	}
	before, _ := e.repo.Repository.GetNode(context.Background(), id)
	e.clk.Advance(time.Second)
	// A rejected (429) registration writes nothing, not even display_name or last_seen_at (N15).
	renamed := strings.Replace(nodeBody(id, 200), `"desk"`, `"renamed-by-429"`, 1)
	st, hdr, b := e.post(t, "/v1/nodes/register", renamed)
	if after, _ := e.repo.Repository.GetNode(context.Background(), id); after.DisplayName != before.DisplayName || !after.LastSeenAt.Equal(before.LastSeenAt) {
		t.Fatalf("a 429 registration wrote the node row: %q %v -> %q %v", before.DisplayName, before.LastSeenAt, after.DisplayName, after.LastSeenAt)
	}
	if st != 429 || codeOf(t, b) != "RATE_LIMITED" || hdr.Get("Retry-After") != "49" {
		t.Fatalf("changed hardware inside the interval: %d Retry-After=%q %s, want 429 RATE_LIMITED 49", st, hdr.Get("Retry-After"), b)
	}
	for _, s := range []string{"100", "memory", "cpu", "linux"} {
		if strings.Contains(b, s) {
			t.Errorf("429 body is not opaque, contains %q: %s", s, b)
		}
	}
	e.clk.Advance(49 * time.Second)
	if st, _, b := e.post(t, "/v1/nodes/register", nodeBody(id, 200)); st != 200 || !strings.Contains(b, `"hardware_version":2`) {
		t.Fatalf("changed hardware after the interval: %d %s, want 200 version 2", st, b)
	}
}

func TestAPI_NodeTelemetry_OutOfOrder409(t *testing.T) {
	e := newNodeEnv(t, true)
	id := nid(1)
	e.mustRegisterNode(t, id, 100)
	path := "/v1/nodes/" + id + "/telemetry"
	if st, _, b := e.post(t, path, telBody(nid(100), 2, 0)); st != 200 || b != `{"status":"ACK"}`+"\n" {
		t.Fatalf("first report: %d %q", st, b)
	}
	before, _ := e.repo.Repository.GetNode(context.Background(), id)
	e.clk.Advance(time.Second)
	for _, seq := range []int64{2, 1} {
		st, _, b := e.post(t, path, strings.Replace(telBody(nid(100), seq, 0), `"cpu_load_1m_milli":851`, `"cpu_load_1m_milli":999`, 1))
		if st != 409 || codeOf(t, b) != "TELEMETRY_OUT_OF_ORDER" {
			t.Fatalf("seq %d: %d %s, want 409 TELEMETRY_OUT_OF_ORDER", seq, st, b)
		}
		if strings.Contains(b, "stored") || strings.Contains(b, "2") && strings.Contains(b, "seq") {
			t.Errorf("409 body is not opaque: %s", b)
		}
	}
	after, _ := e.repo.Repository.GetNode(context.Background(), id)
	if *after.Telemetry.CPULoad1mMilli != 851 || !after.Telemetry.ReceivedAt.Equal(before.Telemetry.ReceivedAt) {
		t.Fatalf("a rejected report changed the stored sample: %+v", after.Telemetry)
	}
	bad := map[string]string{
		"missing session": `{"contract_version":1,"seq":5}`,
		"empty session":   `{"contract_version":1,"session_id":"","seq":5}`,
		"malformed":       `{"contract_version":1,"session_id":"nope","seq":5}`,
		"uppercase":       `{"contract_version":1,"session_id":"` + strings.ToUpper(nid(100)) + `","seq":5}`,
		"missing seq":     `{"contract_version":1,"session_id":"` + nid(100) + `"}`,
		"seq zero":        `{"contract_version":1,"session_id":"` + nid(100) + `","seq":0}`,
		"seq negative":    `{"contract_version":1,"session_id":"` + nid(100) + `","seq":-3}`,
		"seq above 2^53":  `{"contract_version":1,"session_id":"` + nid(100) + `","seq":9007199254740992}`,
		"seq string":      `{"contract_version":1,"session_id":"` + nid(100) + `","seq":"5"}`,
	}
	for name, body := range bad {
		if st, _, b := e.post(t, path, body); st != 400 {
			t.Errorf("%s: %d %s, want 400", name, st, b)
		}
	}
	if st, _, b := e.post(t, path, `{"contract_version":2,"session_id":"`+nid(100)+`","seq":9}`); st != 400 || codeOf(t, b) != "UNSUPPORTED_CONTRACT_VERSION" {
		t.Fatalf("contract 2: %d %s", st, b)
	}
}

func TestAPI_NodeRoutes_NotServedWhenNotLocalOnly(t *testing.T) {
	e := newNodeEnv(t, false)
	node := nid(1)
	_, err := e.repo.Repository.RegisterNode(context.Background(), domain.NodeRegistration{NodeID: node, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// A row that carries a node id (rollback or manual insert) must not leak it.
	if err := e.repo.Repository.UpsertWorker(context.Background(), &domain.Worker{ID: "w1", TotalSlots: 1, Status: domain.WorkerStatusHealthy, NodeID: &node}); err != nil {
		t.Fatal(err)
	}
	routes := []struct{ method, path, body string }{
		{"POST", "/v1/nodes/register", nodeBody(node, 1)},
		{"POST", "/v1/nodes/" + node + "/telemetry", telBody(nid(100), 1, 0)},
		{"GET", "/v1/nodes", ""},
		{"GET", "/v1/nodes/" + node, ""},
	}
	for _, r := range routes {
		if st, _, b := e.do(t, r.method, r.path, r.body, nil); st != 404 || strings.Contains(b, "NODE_NOT_FOUND") {
			t.Errorf("%s %s: %d %s, want the router's plain 404", r.method, r.path, st, b)
		}
	}
	_, _, b := e.get(t, "/v1/workers")
	ws := decodeWorkers(t, b)
	if len(ws) != 1 {
		t.Fatalf("workers: %s", b)
	}
	if _, present := ws[0]["node_id"]; present || strings.Contains(b, node) {
		t.Fatalf("/v1/workers shows node_id when not local-only: %s", b)
	}
}

func TestAPI_RegisterWorker_NodeIDIgnoredWhenNotLocalOnly(t *testing.T) {
	e := newNodeEnv(t, false)
	known := nid(1)
	if _, err := e.repo.Repository.RegisterNode(context.Background(), domain.NodeRegistration{NodeID: known, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}, time.Minute); err != nil {
		t.Fatal(err)
	}
	wantResp := `{"heartbeat_interval_seconds":2,"status":"REGISTERED"}` + "\n"
	for i, nj := range []string{"", "null", `"` + known + `"`, `"` + nid(9) + `"`, `"junk"`, `5`, `true`, `{"a":1}`, `["x"]`} {
		st, _, b := e.post(t, "/v1/workers/register", workerBody("w1", nj))
		if st != 200 || b != wantResp {
			t.Fatalf("case %d node_id=%s: %d %q, want 200 %q (byte-identical to the v1 response)", i, nj, st, b, wantResp)
		}
		w, _ := e.repo.Repository.GetWorker(context.Background(), "w1")
		if w.NodeID != nil {
			t.Fatalf("case %d: stored node_id = %q, want NULL", i, *w.NodeID)
		}
	}
	// The worker list is byte-identical to the v1 shape.
	_, _, b := e.get(t, "/v1/workers")
	ts := e.clk.Now().UTC().Format(time.RFC3339Nano)
	want := fmt.Sprintf(`{"workers":[{"id":"w1","version":"v","status":"HEALTHY","total_slots":2,"available_slots":2,"supported_tasks":["echo"],"last_heartbeat_at":%q,"registered_at":%q,"executes_concurrently":false}]}`+"\n", ts, ts)
	if b != want {
		t.Fatalf("worker list is not the v1 shape:\n got %s\nwant %s", b, want)
	}
}

func TestAPI_NodeRoutes_IgnoreForwardedHeaders(t *testing.T) {
	hdrs := []map[string]string{
		{"X-Forwarded-For": "8.8.8.8"},
		{"X-Forwarded-For": "127.0.0.1", "X-Real-IP": "127.0.0.1"},
		{"X-Real-IP": "203.0.113.9", "Forwarded": "for=203.0.113.9"},
		{"Host": "evil.example.com"},
		{"Host": "localhost:8080"},
		{"Host": "127.0.0.1:8080", "X-Forwarded-Host": "example.com"},
	}
	closed := newNodeEnv(t, false)
	open := newNodeEnv(t, true)
	for _, h := range hdrs {
		if st, _, b := closed.do(t, "GET", "/v1/nodes", "", h); st != 404 {
			t.Errorf("not local-only with headers %v: %d %s, want 404", h, st, b)
		}
		if st, _, _ := closed.do(t, "POST", "/v1/nodes/register", nodeBody(nid(1), 1), h); st != 404 {
			t.Errorf("not local-only register with headers %v: %d, want 404", h, st)
		}
		if st, _, b := open.do(t, "GET", "/v1/nodes", "", h); st != 200 {
			t.Errorf("local-only with headers %v: %d %s, want 200", h, st, b)
		}
		if st, _, b := open.do(t, "POST", "/v1/nodes/register", nodeBody(nid(1), 1), h); st != 200 {
			t.Errorf("local-only register with headers %v: %d %s, want 200", h, st, b)
		}
	}
}

func TestAPI_ListNodes_Paging(t *testing.T) {
	e := newNodeEnv(t, true)
	for _, n := range []int{3, 1, 5, 2, 4} {
		e.mustRegisterNode(t, nid(n), int64(100+n))
	}
	type page struct {
		Nodes []struct {
			NodeID string `json:"node_id"`
		} `json:"nodes"`
		NextCursor *string `json:"next_cursor"`
	}
	fetch := func(q string) (int, page, string) {
		st, _, b := e.get(t, "/v1/nodes"+q)
		var p page
		_ = json.Unmarshal([]byte(b), &p)
		return st, p, b
	}
	var seen []string
	cursor := ""
	for i := 0; i < 5; i++ {
		q := "?limit=2"
		if cursor != "" {
			q += "&after=" + cursor
		}
		st, p, b := fetch(q)
		if st != 200 {
			t.Fatalf("%s: %d %s", q, st, b)
		}
		for _, n := range p.Nodes {
			seen = append(seen, n.NodeID)
		}
		if p.NextCursor == nil {
			break
		}
		if len(p.Nodes) != 2 || *p.NextCursor != p.Nodes[1].NodeID {
			t.Fatalf("next_cursor %v does not equal the last node_id of a full page: %s", p.NextCursor, b)
		}
		cursor = *p.NextCursor
	}
	if strings.Join(seen, ",") != strings.Join([]string{nid(1), nid(2), nid(3), nid(4), nid(5)}, ",") {
		t.Fatalf("paged order = %v", seen)
	}
	if st, p, b := fetch("?limit=3&after=" + nid(3)); st != 200 || len(p.Nodes) != 2 || p.NextCursor != nil || !strings.Contains(b, `"next_cursor":null`) {
		t.Fatalf("last page: %d %s, want 2 nodes and next_cursor null", st, b)
	}
	if st, p, _ := fetch(""); st != 200 || len(p.Nodes) != 5 || p.NextCursor != nil {
		t.Fatalf("default limit: %d %+v", st, p)
	}
	if st, p, _ := fetch("?limit=500"); st != 200 || len(p.Nodes) != 5 {
		t.Fatalf("limit=500: %d %+v", st, p)
	}
	for _, q := range []string{"?limit=0", "?limit=501", "?limit=-1", "?limit=abc", "?limit=", "?limit=1.5", "?limit=%2B5", "?limit=99999999999999999999",
		"?after=x", "?after=", "?after=" + strings.ToUpper(nid(1)), "?after=abc00001-0000-4000-8000-0000000000%00", "?limit=2&after=" + nid(1) + "x"} {
		if st, _, b := fetch(q); st != 400 || codeOf(t, b) != "INVALID_REQUEST" {
			t.Errorf("%s: %d %s, want 400 INVALID_REQUEST", q, st, b)
		}
	}
	empty := newNodeEnv(t, true)
	if _, _, b := empty.get(t, "/v1/nodes"); b != `{"next_cursor":null,"nodes":[]}`+"\n" {
		t.Fatalf("empty list body = %q", b)
	}
}

func TestAPI_RegisterNode_NodeLimitReached(t *testing.T) {
	e := newNodeEnv(t, true)
	if err := e.svc.SetNodeSettings(time.Minute, 2); err != nil {
		t.Fatal(err)
	}
	e.mustRegisterNode(t, nid(1), 100)
	e.mustRegisterNode(t, nid(2), 100)
	st, hdr, b := e.post(t, "/v1/nodes/register", nodeBody(nid(3), 100))
	if st != 429 || codeOf(t, b) != "NODE_LIMIT_REACHED" || hdr.Get("Retry-After") != "" {
		t.Fatalf("third node: %d Retry-After=%q %s, want 429 NODE_LIMIT_REACHED without Retry-After", st, hdr.Get("Retry-After"), b)
	}
	if n, _ := e.repo.Repository.CountNodes(context.Background()); n != 2 {
		t.Fatalf("nodes = %d, want 2", n)
	}
	// An existing node still registers (idempotent, with a rename) and still reports telemetry.
	renamed := strings.Replace(nodeBody(nid(1), 100), `"desk"`, `"renamed"`, 1)
	if st, _, b := e.post(t, "/v1/nodes/register", renamed); st != 200 {
		t.Fatalf("existing node re-registration: %d %s", st, b)
	}
	if st, _, b := e.post(t, "/v1/nodes/"+nid(2)+"/telemetry", telBody(nid(100), 1, 0)); st != 200 {
		t.Fatalf("telemetry on an existing node: %d %s", st, b)
	}
	// A changed hardware body on an existing node meets the rate limit, not the ceiling.
	if st, _, b := e.post(t, "/v1/nodes/register", nodeBody(nid(1), 999)); st != 429 || codeOf(t, b) != "RATE_LIMITED" {
		t.Fatalf("existing node, changed hardware: %d %s, want RATE_LIMITED", st, b)
	}
}

func TestAPI_RegisterNode_NULByteIDNeverReachesRepository(t *testing.T) {
	e := newNodeEnv(t, true)
	nul := `abc00001-0000-4000-8000-0000000000\u0000`
	check := func(name, method, path, body string, want int) {
		t.Helper()
		before := e.repo.calls.Load()
		st, _, b := e.do(t, method, path, body, nil)
		if st != want {
			t.Errorf("%s: %d %s, want %d", name, st, b, want)
		}
		if e.repo.calls.Load() != before {
			t.Errorf("%s: a repository method was called %d time(s) for an invalid ID", name, e.repo.calls.Load()-before)
		}
	}
	check("register body", "POST", "/v1/nodes/register", strings.Replace(nodeBody(nid(1), 1), nid(1), nul, 1), 400)
	check("register body, 36 bytes with NUL", "POST", "/v1/nodes/register", strings.Replace(nodeBody(nid(1), 1), nid(1), `abc00001-0000-4000-8000-00000000000\u0000`, 1), 400)
	check("telemetry path", "POST", "/v1/nodes/abc00001-0000-4000-8000-0000000000%00/telemetry", telBody(nid(100), 1, 0), 400)
	check("get path", "GET", "/v1/nodes/abc00001-0000-4000-8000-0000000000%00", "", 400)
	check("list cursor", "GET", "/v1/nodes?after=abc00001-0000-4000-8000-0000000000%00", "", 400)
	check("worker register node_id", "POST", "/v1/workers/register", workerBody("w1", `"`+nul+`"`), 400)
	if bytes.Contains([]byte(nul), []byte{0}) {
		t.Fatal("test bug: the literal must be a JSON escape")
	}
}
