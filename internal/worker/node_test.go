package worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/platform"
	"github.com/BlaZe08-Dev/Minicloud/internal/version"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker/workertest"
)

const (
	ntWait     = 10 * time.Second // failure bound of every wait, never a timing assertion
	ntNodeReg  = "POST /v1/nodes/register"
	ntWorkReg  = "POST /v1/workers/register"
	ntPollPath = "/v1/workers/w1/poll"
	ntSecret   = "UNIQ-CPU-MODEL-xyz"
)

type ntReq struct{ method, path, body string }

func (r ntReq) key() string { return r.method + " " + r.path }

// ntReply is one scripted answer; the zero value means "the default answer for this path".
type ntReply struct {
	status int
	header map[string]string
	body   string
	block  bool // hold the request until the client gives up (client timeout)
}

type ntRig struct {
	t      *testing.T
	ts     *httptest.Server
	clock  *workertest.FakeClock
	agent  *worker.Agent
	cfg    *config.WorkerConfig
	logs   *syncBuffer
	script func(path string, hit int, body string) ntReply

	mu       sync.Mutex
	reqs     []ntReq
	hits     map[string]int
	consumed int

	cancel context.CancelFunc
	done   chan error
}

// ntProbe is a fake platform probe with recognisable values.
type ntProbe struct {
	telemetryCalls int
	panicFirst     bool
	mu             sync.Mutex
}

func (p *ntProbe) Hardware(context.Context) (domain.NodeHardware, error) {
	m, n := ntSecret, int64(424242)
	return domain.NodeHardware{OS: "linux", Arch: "amd64", CPUModel: &m, CPULogicalCores: &n}, nil
}

func (p *ntProbe) Telemetry(context.Context) (domain.NodeTelemetry, error) {
	p.mu.Lock()
	p.telemetryCalls++
	first := p.telemetryCalls == 1
	p.mu.Unlock()
	if p.panicFirst && first {
		panic("probe boom")
	}
	l, m := int64(424243), int64(7777777)
	return domain.NodeTelemetry{CPULoad1mMilli: &l, MemoryAvailableBytes: &m}, nil
}

func ntLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func ntSetup(t *testing.T, cfg config.WorkerConfig, script func(path string, hit int, body string) ntReply) *ntRig {
	t.Helper()
	r := &ntRig{t: t, script: script, hits: map[string]int{}, logs: ntLogs(t), clock: workertest.NewFakeClock(time.Unix(1_700_000_000, 0))}
	r.ts = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.ts.Close)
	cfg.WorkerID, cfg.CoordinatorURL, cfg.TotalSlots, cfg.HeartbeatInterval = "w1", r.ts.URL, 1, time.Hour
	r.cfg = &cfg
	r.agent = worker.NewAgent(r.cfg, executor.NewRegistry())
	r.agent.SetClock(r.clock)
	worker.SetProbeFactoryForTest(r.agent, func(string) platform.Probe { return &ntProbe{} })
	return r
}

func (r *ntRig) serve(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(req.Body)
	q := ntReq{req.Method, req.URL.Path, string(raw)}
	r.mu.Lock()
	r.reqs = append(r.reqs, q)
	hit := r.hits[q.key()]
	r.hits[q.key()]++
	r.mu.Unlock()
	var rep ntReply
	if r.script != nil {
		rep = r.script(req.URL.Path, hit, q.body)
	}
	if rep.block {
		select {
		case <-req.Context().Done():
		case <-time.After(ntWait):
		}
		return
	}
	if rep.status == 0 {
		rep = ntDefault(req.URL.Path)
	}
	for k, v := range rep.header {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func ntDefault(path string) ntReply {
	switch {
	case path == ntPollPath:
		return ntReply{status: http.StatusNoContent}
	case path == "/v1/nodes/register":
		return ntReply{status: 200, body: `{"status":"REGISTERED"}`}
	case strings.HasSuffix(path, "/telemetry"):
		return ntReply{status: 200, body: `{"status":"ACK"}`}
	}
	return ntReply{status: 200, body: `{"status":"ACK"}`}
}

func ntErr(status int, code string, hdr map[string]string) ntReply {
	return ntReply{status: status, header: hdr, body: fmt.Sprintf(`{"error":{"code":%q,"message":"x"}}`, code)}
}

func (r *ntRig) start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan error, 1)
	go func() { r.done <- r.agent.Run(ctx) }()
	r.t.Cleanup(func() { r.stop() })
}

// runExpectingError runs a worker that must fail at startup. A run that keeps going (a fallback to
// legacy mode) is cancelled and reported as a failure instead of hanging the test.
func (r *ntRig) runExpectingError() error {
	r.t.Helper()
	r.start()
	select {
	case err := <-r.done:
		r.cancel = nil
		return err
	case <-time.After(3 * time.Second):
		r.stop()
		r.t.Fatal("Run kept running; a startup error was expected")
		return nil
	}
}

func (r *ntRig) stop() {
	if r.cancel == nil {
		return
	}
	r.cancel()
	deadline := time.After(ntWait)
	for {
		select {
		case err := <-r.done:
			if err != nil {
				r.t.Errorf("Run returned %v", err)
			}
			r.cancel = nil
			return
		case <-deadline:
			r.t.Error("Run did not return after cancel")
			r.cancel = nil
			return
		default:
			r.clock.Advance(time.Hour) // the work loop's idle sleep waits on the fake clock only
			time.Sleep(time.Millisecond)
		}
	}
}

func (r *ntRig) all() []ntReq {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.reqs)
}

// flow lists the requests that are not polls or heartbeats, in arrival order, as "METHOD path".
func (r *ntRig) flow() []string {
	var out []string
	for _, q := range r.all() {
		if q.path == ntPollPath || strings.HasSuffix(q.path, "/heartbeat") {
			continue
		}
		p := q.path
		if strings.HasSuffix(p, "/telemetry") {
			p = "/telemetry"
		}
		out = append(out, q.method+" "+p)
	}
	return out
}

func (r *ntRig) bodies(key string) []string {
	var out []string
	for _, q := range r.all() {
		if q.key() == key || (key == "telemetry" && strings.HasSuffix(q.path, "/telemetry")) {
			out = append(out, q.body)
		}
	}
	return out
}

func (r *ntRig) count(key string) int { return len(r.bodies(key)) }

func (r *ntRig) nodeRequests() int {
	n := 0
	for _, q := range r.all() {
		if strings.HasPrefix(q.path, "/v1/nodes/") {
			n++
		}
	}
	return n
}

func ntWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(ntWait)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func (r *ntRig) waitCount(key string, n int) {
	r.t.Helper()
	ntWaitFor(r.t, fmt.Sprintf("%d x %s", n, key), func() bool { return r.count(key) >= n })
}

// nodeTimers lists the timers the node goroutine created (the work loop's 200 ms idle sleep is the
// only other timer of these tests).
func (r *ntRig) nodeTimers() []time.Duration {
	var out []time.Duration
	for _, d := range r.clock.Requested() {
		if d != worker.IdlePollInterval {
			out = append(out, d)
		}
	}
	return out
}

// tickNext waits for the next node timer, advances the clock by exactly its duration and returns it.
func (r *ntRig) tickNext() time.Duration {
	r.t.Helper()
	ntWaitFor(r.t, "a node timer", func() bool { return len(r.nodeTimers()) > r.consumed })
	d := r.nodeTimers()[r.consumed]
	r.consumed++
	r.clock.Advance(d)
	return d
}

// advanceUntilPoll advances the clock (repeatedly: the poll timer may not exist yet) until one more
// poll arrives. Only for runs that have no node timers.
func (r *ntRig) advanceUntilPoll(d time.Duration) {
	r.t.Helper()
	before := r.polls()
	ntWaitFor(r.t, "a poll after the advance", func() bool {
		r.clock.Advance(d)
		return r.polls() > before
	})
}

func (r *ntRig) polls() int { return r.count("POST " + ntPollPath) }

// ntDir is a state directory that does not exist yet: the platform code creates it with mode 0700
// and refuses an existing one that is wider (t.TempDir is 0775).
func ntDir(t *testing.T) string { return filepath.Join(t.TempDir(), "state") }

func ntOn(dir string, interval time.Duration) config.WorkerConfig {
	return config.WorkerConfig{NodeReportingEnabled: true, NodeStateDir: dir, NodeTelemetryInterval: interval}
}

func ntField(t *testing.T, body, key string) any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return m[key]
}

func ntSeq(t *testing.T, body string) int {
	return int(ntField(t, body, "seq").(float64))
}

func TestWorker_RegistersNodeThenSelf(t *testing.T) {
	dir := ntDir(t)
	r := ntSetup(t, ntOn(dir, 10*time.Second), nil)
	r.start()
	r.waitCount("telemetry", 1)

	want := []string{ntNodeReg, ntWorkReg, "POST /telemetry"}
	if got := r.flow(); !slices.Equal(got, want) {
		t.Fatalf("flow = %v, want %v", got, want)
	}
	idFile, err := os.ReadFile(filepath.Join(dir, "node_id"))
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(idFile))
	nodeBody, workerBody, telBody := r.bodies(ntNodeReg)[0], r.bodies(ntWorkReg)[0], r.bodies("telemetry")[0]
	if ntField(t, nodeBody, "node_id") != id || ntField(t, workerBody, "node_id") != id {
		t.Fatalf("node id mismatch: node body %s, worker body %s, file %s", nodeBody, workerBody, id)
	}
	if ntField(t, nodeBody, "display_name") != "" || ntField(t, nodeBody, "agent_version") != version.Version || ntField(t, nodeBody, "contract_version") != float64(domain.ContractVersion) {
		t.Fatalf("node registration body = %s", nodeBody)
	}
	if hw, _ := ntField(t, nodeBody, "hardware").(map[string]any); hw["cpu_model"] != ntSecret {
		t.Fatalf("hardware = %v", hw)
	}
	if err := domain.ValidateSessionID(ntField(t, telBody, "session_id").(string)); err != nil || ntSeq(t, telBody) != 1 {
		t.Fatalf("telemetry body = %s (%v)", telBody, err)
	}
	if ntField(t, telBody, "cpu_load_1m_milli") != float64(424243) {
		t.Fatalf("telemetry body = %s", telBody)
	}
}

func TestWorker_ReportingOff_LegacyRegistration(t *testing.T) {
	r := ntSetup(t, config.WorkerConfig{}, nil)
	var probeCalls int
	var mu sync.Mutex
	worker.SetProbeFactoryForTest(r.agent, func(string) platform.Probe {
		mu.Lock()
		probeCalls++
		mu.Unlock()
		t.Error("the probe must not be built with node reporting off")
		return &ntProbe{}
	})
	r.start()
	ntWaitFor(t, "a poll", func() bool { return r.polls() >= 1 })
	r.stop()

	// The body origin/main sends, byte for byte (the version string is the build's).
	want := fmt.Sprintf(`{"available_slots":1,"executes_concurrently":true,"id":"w1","supported_tasks":["echo","sleep","sha256_loop"],"total_slots":1,"version":%q}`, version.Version)
	if got := r.bodies(ntWorkReg); len(got) != 1 || got[0] != want {
		t.Fatalf("register bodies = %q, want [%q]", got, want)
	}
	if n := r.nodeRequests(); n != 0 {
		t.Fatalf("node requests = %d, want 0", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if probeCalls != 0 {
		t.Fatalf("probe built %d times", probeCalls)
	}
}

func TestWorker_StateDirSetButReportingOff_NothingReadCreatedOrSent(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("mode 000 does not stop root")
	}
	t.Run("existing directory mode 000", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "state")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
		ntStateDirOff(t, dir, func() {
			info, err := os.Stat(dir)
			if err != nil || info.Mode().Perm() != 0 {
				t.Fatalf("directory changed: %v %v", info, err)
			}
			_ = os.Chmod(dir, 0o700)
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Fatalf("directory was written to: %v", entries)
			}
		})
	})
	t.Run("missing directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "never", "created")
		ntStateDirOff(t, dir, func() {
			if _, err := os.Lstat(filepath.Dir(dir)); !os.IsNotExist(err) {
				t.Fatalf("the path was created (lstat err = %v)", err)
			}
		})
	})
}

func ntStateDirOff(t *testing.T, dir string, check func()) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := ntSetup(t, config.WorkerConfig{NodeStateDir: dir}, nil)
	worker.SetProbeFactoryForTest(r.agent, func(string) platform.Probe {
		t.Error("the probe must not be built with node reporting off")
		return &ntProbe{}
	})
	r.start()
	ntWaitFor(t, "a poll", func() bool { return r.polls() >= 1 })
	r.stop()
	check()
	if n := r.nodeRequests(); n != 0 {
		t.Fatalf("node requests = %d", n)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("home was written to: %v", entries)
	}
	warns := 0
	for _, line := range strings.Split(strings.TrimSpace(r.logs.String()), "\n") {
		if strings.Contains(line, dir) {
			t.Fatalf("a log line carries the path: %s", line)
		}
		if strings.Contains(line, `"level":"WARN"`) && strings.Contains(line, "MINICLOUD_NODE_STATE_DIR") {
			warns++
			if !strings.Contains(line, "MINICLOUD_NODE_REPORTING_ENABLED") {
				t.Fatalf("warning does not name both settings: %s", line)
			}
		}
	}
	if warns != 1 {
		t.Fatalf("warning lines = %d, want exactly 1\n%s", warns, r.logs.String())
	}
}

func TestWorker_ReportingOn_DefaultStateDirIsHomeMinicloud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	r := ntSetup(t, ntOn("", 0), nil)
	r.start()
	r.waitCount(ntWorkReg, 1)
	r.stop()
	dir := filepath.Join(home, ".minicloud")
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("%s: %v %v, want mode 0700", dir, info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "node_id")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("node_id: %v %v, want mode 0600", info, err)
	}
}

func TestWorker_ReportingOn_NoHomeNoDir_StartupError(t *testing.T) {
	for _, unset := range []bool{true, false} {
		t.Setenv("HOME", "")
		if unset {
			os.Unsetenv("HOME")
		}
		r := ntSetup(t, ntOn("", 10*time.Second), nil)
		err := r.runExpectingError()
		if err == nil || !strings.Contains(err.Error(), "MINICLOUD_NODE_REPORTING_ENABLED") || !strings.Contains(err.Error(), "MINICLOUD_NODE_STATE_DIR") {
			t.Fatalf("unset=%v: Run error = %v, want one naming both settings", unset, err)
		}
		if n := len(r.all()); n != 0 {
			t.Fatalf("unset=%v: %d requests sent, want 0", unset, n)
		}
	}
}

func TestWorker_StartupErrors_NoFallback(t *testing.T) {
	t.Run("relative state dir is a config error", func(t *testing.T) {
		t.Setenv("MINICLOUD_NODE_REPORTING_ENABLED", "true")
		t.Setenv("MINICLOUD_NODE_STATE_DIR", "relative/dir")
		if _, err := config.LoadWorkerConfig(); err == nil || !strings.Contains(err.Error(), "MINICLOUD_NODE_STATE_DIR") {
			t.Fatalf("err = %v", err)
		}
	})
	if os.Getuid() == 0 {
		t.Skip("permission checks do not stop root")
	}
	const goodID = "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23"
	cases := map[string]func(t *testing.T, parent string) string{
		"unwritable dir": func(t *testing.T, parent string) string {
			if err := os.Chmod(parent, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
			return filepath.Join(parent, "state")
		},
		"corrupt node_id": func(t *testing.T, parent string) string {
			dir := filepath.Join(parent, "state")
			_ = os.Mkdir(dir, 0o700)
			if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte("garbage\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return dir
		},
		"node_id mode 0644": func(t *testing.T, parent string) string {
			dir := filepath.Join(parent, "state")
			_ = os.Mkdir(dir, 0o700)
			if err := os.WriteFile(filepath.Join(dir, "node_id"), []byte(goodID+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(dir, "node_id"), 0o644); err != nil {
				t.Fatal(err)
			}
			return dir
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			parent := t.TempDir()
			dir := mk(t, parent)
			r := ntSetup(t, ntOn(dir, 10*time.Second), nil)
			if err := r.runExpectingError(); err == nil {
				t.Fatal("Run returned nil, want a startup error")
			}
			if n := len(r.all()); n != 0 {
				t.Fatalf("%d requests sent, want 0", n)
			}
			if entries, _ := os.ReadDir(home); len(entries) != 0 {
				t.Fatalf("an identity or directory was created in HOME: %v", entries)
			}
			if _, err := os.Stat(filepath.Join(parent, "node_id")); err == nil {
				t.Fatal("a node_id was created next to the state directory")
			}
		})
	}
}

func TestWorker_ReportingOn_NoTelemetryIntervalSendsNoTelemetry(t *testing.T) {
	r := ntSetup(t, ntOn(ntDir(t), 0), nil)
	r.start()
	r.waitCount(ntWorkReg, 1)
	for i := 0; i < 5; i++ {
		r.advanceUntilPoll(24 * time.Hour)
	}
	r.stop()
	if got := r.flow(); !slices.Equal(got, []string{ntNodeReg, ntWorkReg}) {
		t.Fatalf("flow = %v, want node register then worker register only", got)
	}
	if !strings.Contains(r.bodies(ntWorkReg)[0], `"node_id"`) {
		t.Fatal("the worker registration did not carry node_id")
	}
	infos := 0
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if strings.Contains(line, `"level":"INFO"`) && strings.Contains(line, "MINICLOUD_NODE_TELEMETRY_INTERVAL") {
			infos++
		}
	}
	if infos != 1 {
		t.Fatalf("info lines about the unset interval = %d, want 1", infos)
	}
}

func TestWorker_TelemetrySeqIncreasesAndRetryUsesNewSeq(t *testing.T) {
	// Telemetry answers by hit: 0-2 ok, 3 server error, 4 client timeout, 5 ok, 6 out of order, 7+ ok.
	r := ntSetup(t, ntOn(ntDir(t), 10*time.Second), func(path string, hit int, _ string) ntReply {
		if !strings.HasSuffix(path, "/telemetry") {
			return ntReply{}
		}
		switch hit {
		case 3:
			return ntErr(500, "INTERNAL_ERROR", nil)
		case 4:
			return ntReply{block: true}
		case 6:
			return ntErr(409, "TELEMETRY_OUT_OF_ORDER", nil)
		}
		return ntReply{}
	})
	worker.SetHTTPTimeoutForTest(r.agent, 200*time.Millisecond)
	r.start()
	for i := 1; i <= 9; i++ {
		r.waitCount("telemetry", i)
		if i < 9 {
			if d := r.tickNext(); d != 10*time.Second {
				t.Fatalf("wait after report %d = %v, want the 10 s interval", i, d)
			}
		}
	}
	r.stop()
	bodies := r.bodies("telemetry")
	var seqs []int
	var sessions []string
	for _, b := range bodies {
		seqs = append(seqs, ntSeq(t, b))
		sessions = append(sessions, ntField(t, b, "session_id").(string))
	}
	if want := []int{1, 2, 3, 4, 5, 6, 7, 1, 2}; !slices.Equal(seqs, want) {
		t.Fatalf("seqs = %v, want %v", seqs, want)
	}
	for i := 1; i < 7; i++ {
		if sessions[i] != sessions[0] {
			t.Fatalf("report %d left the session", i)
		}
	}
	if sessions[7] == sessions[0] || domain.ValidateSessionID(sessions[7]) != nil || sessions[8] != sessions[7] {
		t.Fatalf("session after 409 = %q (first %q)", sessions[7], sessions[0])
	}
	for i, b := range bodies {
		if i != 6 && b == bodies[6] {
			t.Fatalf("the rejected body was sent again as report %d", i)
		}
		if i > 0 && b == bodies[i-1] {
			t.Fatalf("report %d is identical to the previous one", i)
		}
	}
}

// ntLegacy runs a worker against a node register answer that means "legacy for life".
func ntLegacy(t *testing.T, rep ntReply) {
	t.Helper()
	r := ntSetup(t, ntOn(ntDir(t), 10*time.Second), func(path string, _ int, _ string) ntReply {
		if path == "/v1/nodes/register" {
			return rep
		}
		return ntReply{}
	})
	r.start()
	r.waitCount(ntWorkReg, 1)
	if strings.Contains(r.bodies(ntWorkReg)[0], "node_id") {
		t.Fatalf("legacy worker registered with node_id: %s", r.bodies(ntWorkReg)[0])
	}
	for i := 0; i < 5; i++ {
		r.advanceUntilPoll(24 * time.Hour)
	}
	r.stop()
	if got := r.flow(); !slices.Equal(got, []string{ntNodeReg, ntWorkReg}) {
		t.Fatalf("flow = %v, want one node register and one worker register", got)
	}
	lines := 0
	for _, line := range strings.Split(r.logs.String(), "\n") {
		if strings.Contains(line, "node reporting is off for this process") {
			lines++
		}
	}
	if lines != 1 {
		t.Fatalf("legacy log lines = %d, want 1", lines)
	}
}

func TestWorker_Node404_LegacyNoRetry(t *testing.T) {
	t.Run("empty body", func(t *testing.T) { ntLegacy(t, ntReply{status: 404}) })
	t.Run("text body", func(t *testing.T) { ntLegacy(t, ntReply{status: 404, body: "404 page not found\n"}) })
}

func TestWorker_NodeLegacyForLife_OnLimitAnd400(t *testing.T) {
	t.Run("429 NODE_LIMIT_REACHED", func(t *testing.T) { ntLegacy(t, ntErr(429, "NODE_LIMIT_REACHED", nil)) })
	t.Run("400 INVALID_REQUEST", func(t *testing.T) { ntLegacy(t, ntErr(400, "INVALID_REQUEST", nil)) })
	t.Run("400 UNSUPPORTED_CONTRACT_VERSION", func(t *testing.T) { ntLegacy(t, ntErr(400, "UNSUPPORTED_CONTRACT_VERSION", nil)) })
	t.Run("400 MALFORMED_JSON", func(t *testing.T) { ntLegacy(t, ntErr(400, "MALFORMED_JSON", nil)) })
}

func TestWorker_Node429_RegistersWithNodeIDAndRetriesAfter(t *testing.T) {
	cases := []struct {
		name  string
		retry map[string]string
		want  time.Duration
	}{
		{"seven seconds", map[string]string{"Retry-After": "7"}, 7 * time.Second},
		{"absent", nil, 60 * time.Second},
		{"not a number", map[string]string{"Retry-After": "soon"}, 60 * time.Second},
		{"negative", map[string]string{"Retry-After": "-3"}, 60 * time.Second},
		{"huge is capped at 24 h", map[string]string{"Retry-After": "99999999999"}, 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := ntSetup(t, ntOn(ntDir(t), 0), func(path string, hit int, _ string) ntReply {
				if path == "/v1/nodes/register" && hit == 0 {
					return ntErr(429, "RATE_LIMITED", c.retry)
				}
				return ntReply{}
			})
			r.start()
			r.waitCount(ntWorkReg, 1)
			if !strings.Contains(r.bodies(ntWorkReg)[0], `"node_id"`) {
				t.Fatal("the worker did not register with node_id after the 429")
			}
			ntWaitFor(t, "the re-send timer", func() bool { return len(r.nodeTimers()) >= 1 })
			if got := r.nodeTimers()[0]; got != c.want {
				t.Fatalf("re-send timer = %v, want %v", got, c.want)
			}
			r.clock.Advance(c.want - time.Second)
			if n := r.count(ntNodeReg); n != 1 {
				t.Fatalf("node register sent %d times before Retry-After passed", n)
			}
			r.clock.Advance(time.Second)
			r.waitCount(ntNodeReg, 2)
			if n := r.count(ntWorkReg); n != 1 {
				t.Fatalf("worker registered %d times; a 200 re-send needs no new registration", n)
			}
		})
	}
}

func TestWorker_NodeRegisterFails_FallsBackToLegacy(t *testing.T) {
	r := ntSetup(t, ntOn(ntDir(t), 0), func(path string, hit int, _ string) ntReply {
		switch {
		case path == "/v1/nodes/register" && hit == 0:
			return ntErr(500, "INTERNAL_ERROR", nil)
		case path == ntPollPath && hit == 0:
			return ntReply{status: 200, body: mustJSON(map[string]any{"assignment": coordinator.WorkAssignment{
				JobID: "job-1", AttemptID: "att-1", AttemptNo: 1, LeaseToken: "tok", TaskType: "echo",
				Payload: json.RawMessage(`{"m":"hi"}`), TimeoutSeconds: 30}})}
		case strings.HasSuffix(path, "/renew"):
			return ntReply{status: 200, body: `{"status":"RENEWED","cancel_requested":false}`}
		}
		return ntReply{}
	})
	r.start()
	r.waitCount("POST /v1/jobs/job-1/attempts/att-1/result", 1)
	if strings.Contains(r.bodies(ntWorkReg)[0], "node_id") {
		t.Fatalf("first registration carried node_id: %s", r.bodies(ntWorkReg)[0])
	}
	ntWaitFor(t, "the backoff timer", func() bool { return len(r.nodeTimers()) >= 1 })
	d := r.tickNext()
	if d < 500*time.Millisecond || d > time.Second {
		t.Fatalf("first backoff = %v, want within [0.5 s, 1 s)", d)
	}
	r.waitCount(ntWorkReg, 2)
	if !strings.Contains(r.bodies(ntWorkReg)[1], `"node_id"`) {
		t.Fatalf("second registration lacks node_id: %s", r.bodies(ntWorkReg)[1])
	}
	if n := r.count(ntNodeReg); n != 2 {
		t.Fatalf("node registers = %d, want 2 (the failed one and the retry)", n)
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestWorker_RegisterWithNodeID400_FallsBackAndReattaches(t *testing.T) {
	t.Run("recovers", func(t *testing.T) {
		r := ntSetup(t, ntOn(ntDir(t), 0), func(path string, hit int, body string) ntReply {
			if path == "/v1/workers/register" && hit == 0 {
				return ntErr(400, "INVALID_REQUEST", nil)
			}
			return ntReply{}
		})
		r.start()
		r.waitCount(ntWorkReg, 3)
		want := []string{ntNodeReg, ntWorkReg, ntWorkReg, ntNodeReg, ntWorkReg}
		if got := r.flow(); !slices.Equal(got, want) {
			t.Fatalf("flow = %v, want %v", got, want)
		}
		b := r.bodies(ntWorkReg)
		if !strings.Contains(b[0], "node_id") || strings.Contains(b[1], "node_id") || !strings.Contains(b[2], "node_id") {
			t.Fatalf("node_id presence in the three worker registrations is wrong: %q", b)
		}
	})
	t.Run("no loop while the server keeps rejecting node_id", func(t *testing.T) {
		r := ntSetup(t, ntOn(ntDir(t), 0), func(path string, _ int, body string) ntReply {
			if path == "/v1/workers/register" && strings.Contains(body, "node_id") {
				return ntErr(400, "INVALID_REQUEST", nil)
			}
			return ntReply{}
		})
		r.start()
		// The node goroutine ends each failed cycle by arming one backoff timer.
		ntWaitFor(t, "the backoff timer", func() bool { return len(r.nodeTimers()) >= 1 })
		if n, w := r.count(ntNodeReg), r.count(ntWorkReg); n != 2 || w != 4 {
			t.Fatalf("after the first failed cycle: node registers %d, worker registers %d, want 2 and 4", n, w)
		}
		r.tickNext()
		ntWaitFor(t, "the next backoff timer", func() bool { return len(r.nodeTimers()) >= 2 })
		if n, w := r.count(ntNodeReg), r.count(ntWorkReg); n != 3 || w != 6 {
			t.Fatalf("after one more cycle: node registers %d, worker registers %d, want 3 and 6", n, w)
		}
	})
}

func TestWorker_Telemetry404_ReregistersNodeAndSelf(t *testing.T) {
	r := ntSetup(t, ntOn(ntDir(t), 10*time.Second), func(path string, hit int, _ string) ntReply {
		if strings.HasSuffix(path, "/telemetry") && hit == 0 {
			return ntErr(404, "NODE_NOT_FOUND", nil)
		}
		return ntReply{}
	})
	r.start()
	r.waitCount("telemetry", 2)
	want := []string{ntNodeReg, ntWorkReg, "POST /telemetry", ntNodeReg, ntWorkReg, "POST /telemetry"}
	if got := r.flow(); !slices.Equal(got, want) {
		t.Fatalf("flow = %v, want %v", got, want)
	}
	if !strings.Contains(r.bodies(ntWorkReg)[1], `"node_id"`) {
		t.Fatal("the second worker registration lacks node_id")
	}
}

func TestWorker_TelemetryServerErrorBacksOff(t *testing.T) {
	const interval = time.Second
	r := ntSetup(t, ntOn(ntDir(t), interval), func(path string, _ int, _ string) ntReply {
		if strings.HasSuffix(path, "/telemetry") {
			return ntErr(500, "INTERNAL_ERROR", nil)
		}
		return ntReply{}
	})
	r.start()
	const rounds = 8
	pollsBefore := 0
	for i := 0; i < rounds; i++ {
		r.waitCount("telemetry", i+1)
		ntWaitFor(t, "the next report timer", func() bool { return len(r.nodeTimers()) > i })
		if i == 1 {
			pollsBefore = r.polls()
		}
		d := r.nodeTimers()[i]
		if d < interval || d > 30*time.Second {
			t.Fatalf("wait after failure %d = %v, want within [interval, 30 s]", i+1, d)
		}
		r.tickNext()
	}
	if waits := r.nodeTimers(); slices.Max(waits) <= interval {
		t.Fatalf("waits %v never grew past the interval", waits)
	}
	ntWaitFor(t, "polls to continue", func() bool { return r.polls() > pollsBefore })
}

func TestWorker_NodeLogsCarryNoValues(t *testing.T) {
	dir := ntDir(t)
	r := ntSetup(t, ntOn(dir, 10*time.Second), func(path string, hit int, _ string) ntReply {
		switch {
		case path == "/v1/nodes/register" && hit == 0:
			return ntErr(429, "RATE_LIMITED", map[string]string{"Retry-After": "3"})
		case strings.HasSuffix(path, "/telemetry"):
			switch hit {
			case 1:
				return ntErr(409, "TELEMETRY_OUT_OF_ORDER", nil)
			case 2:
				return ntErr(404, "NODE_NOT_FOUND", nil)
			case 3:
				return ntErr(500, "INTERNAL_ERROR", nil)
			}
		}
		return ntReply{}
	})
	r.start()
	// Timers in order: the 3 s re-send, then the 10 s report waits (the 404 re-attaches and the
	// next report follows at once).
	for i := 0; i < 4; i++ {
		r.tickNext()
	}
	r.waitCount("telemetry", 5)
	r.stop()
	if r.count(ntNodeReg) < 2 {
		t.Fatalf("the 404 did not cause a node re-registration: %v", r.flow())
	}
	logs := r.logs.String()
	forbidden := []string{ntSecret, "424242", "424243", "7777777", dir}
	for _, b := range r.bodies("telemetry") {
		forbidden = append(forbidden, ntField(t, b, "session_id").(string))
	}
	for _, f := range forbidden {
		if strings.Contains(logs, f) {
			t.Fatalf("logs contain %q:\n%s", f, logs)
		}
	}
}

func TestWorker_NodePanicIsRecovered(t *testing.T) {
	r := ntSetup(t, ntOn(ntDir(t), 10*time.Second), nil)
	worker.SetProbeFactoryForTest(r.agent, func(string) platform.Probe { return &ntProbe{panicFirst: true} })
	r.start()
	ntWaitFor(t, "the panic log", func() bool { return strings.Contains(r.logs.String(), `"goroutine":"node"`) })
	r.tickNext()
	r.waitCount("telemetry", 1) // the first sample panicked before any send; the next tick reports
}

func TestWorker_NodeGoroutineStopsWithRun(t *testing.T) {
	baseline := runtime.NumGoroutine()
	r := ntSetup(t, ntOn(ntDir(t), 10*time.Second), nil)
	r.start()
	r.waitCount("telemetry", 1)
	r.stop()
	r.ts.Close()
	worker.CloseIdleConnectionsForTest(r.agent)
	deadline := time.Now().Add(ntWait)
	for runtime.NumGoroutine() > baseline {
		if time.Now().After(deadline) {
			buf := make([]byte, 1<<20)
			n := runtime.Stack(buf, true)
			t.Fatalf("goroutines: %d after Run returned, baseline %d\n%s", runtime.NumGoroutine(), baseline, buf[:n])
		}
		time.Sleep(5 * time.Millisecond)
	}
}
