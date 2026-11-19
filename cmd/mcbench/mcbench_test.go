package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPercentile_KnownInputs(t *testing.T) {
	s := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	cases := []struct {
		p    float64
		want float64
	}{{50, 5}, {90, 9}, {95, 10}, {100, 10}, {10, 1}, {1, 1}}
	for _, c := range cases {
		if got := Percentile(s, c.p); got != c.want {
			t.Errorf("p%v = %v, want %v", c.p, got, c.want)
		}
	}
	if !math.IsNaN(Percentile(nil, 50)) {
		t.Error("empty sample must give NaN")
	}
}

func TestSummarize_UnsortedTiesAndSmallN(t *testing.T) {
	in := []float64{9, 1, 5, 5, 3}
	st := Summarize(in)
	if st.N != 5 || st.Min != 1 || st.Median != 5 || st.Max != 9 || st.P95 != 9 || !st.P95IsMax {
		t.Errorf("unexpected stats %+v", st)
	}
	if in[0] != 9 {
		t.Error("Summarize mutated its input")
	}
	one := Summarize([]float64{7})
	if one.Min != 7 || one.Median != 7 || one.Max != 7 || one.N != 1 {
		t.Errorf("N=1: %+v", one)
	}
	two := Summarize([]float64{2, 4})
	if two.Median != 3 {
		t.Errorf("N=2 median = %v, want 3", two.Median)
	}
}

func TestSummarize_P95NotMaxAtN20Plus(t *testing.T) {
	v := make([]float64, 100)
	for i := range v {
		v[i] = float64(i + 1)
	}
	st := Summarize(v)
	if st.P95 != 95 || st.P95IsMax || st.Max != 100 {
		t.Errorf("unexpected stats %+v", st)
	}
}

func TestCheckIdle(t *testing.T) {
	rd := func(s string) func() (string, error) { return func() (string, error) { return s, nil } }
	if _, err := CheckIdle(rd("0.50 0.40 0.30 1/200 999\n"), 1.0); err != nil {
		t.Errorf("below threshold refused: %v", err)
	}
	if _, err := CheckIdle(rd("1.00 0.40 0.30 1/200 999\n"), 1.0); err != nil {
		t.Errorf("at threshold refused: %v", err)
	}
	_, err := CheckIdle(rd("1.01 0.40 0.30 1/200 999\n"), 1.0)
	if err == nil || !strings.Contains(err.Error(), "not idle") {
		t.Errorf("above threshold: %v", err)
	}
	if _, err := CheckIdle(rd("garbage"), 1.0); err == nil {
		t.Error("garbage accepted")
	}
	if _, err := CheckIdle(func() (string, error) { return "", fmt.Errorf("no file") }, 1.0); err == nil {
		t.Error("read failure accepted")
	}
}

func TestCheckDatabaseURL(t *testing.T) {
	ok := "postgres://u:p@127.0.0.1:5433/minicloud_claude_test?sslmode=disable"
	if err := CheckDatabaseURL(ok, "minicloud_claude_test"); err != nil {
		t.Errorf("own database refused: %v", err)
	}
	bad := []struct{ url, allowed string }{
		{"postgres://u:p@127.0.0.1:5433/minicloud_test", "minicloud_claude_test"},
		{"postgres://u:p@127.0.0.1:5433/minicloud_test", "minicloud_test"},
		{"postgres://u:p@127.0.0.1:5433/minicloud", "minicloud"},
		{"postgres://u:p@127.0.0.1:5433/postgres", "minicloud_claude_test"},
		{"postgres://u:p@127.0.0.1:5433/", "minicloud_claude_test"},
		{"postgres://u:p@127.0.0.1:5432/minicloud_claude_test", "minicloud_claude_test"},
		{"postgres://u:p@10.0.0.5:5433/minicloud_claude_test", "minicloud_claude_test"},
		{"postgres://u:p@localhost:5433/minicloud_claude_test", "minicloud_claude_test"},
		{"::not a url", "minicloud_claude_test"},
		{ok, ""},
		{ok, "minicloud_claude"},
	}
	for _, c := range bad {
		if err := CheckDatabaseURL(c.url, c.allowed); err == nil {
			t.Errorf("accepted %q allowed=%q", c.url, c.allowed)
		}
	}
}

func TestCheckLoopbackURL(t *testing.T) {
	if err := CheckLoopbackURL("http://127.0.0.1:18080"); err != nil {
		t.Error(err)
	}
	for _, u := range []string{"http://10.0.0.1:8080", "http://example.com", "http://0.0.0.0:8080", "%%"} {
		if err := CheckLoopbackURL(u); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
}

func completeEnv() Environment {
	return Environment{Commit: "abc", GoVersion: "go1", OS: "Linux", Kernel: "6", CPUModel: "cpu", CPUCores: 4,
		RAMTotalKB: 1000, PostgresVer: "16", Policy: "round_robin", Slots: 4, Workers: 2, N: 5,
		LoadBefore: LoadAvg{0.1, 0.1, 0.1}, LoadAfter: LoadAvg{0.2, 0.1, 0.1}, StartedAt: "t0", IdleCheck: "passed"}
}

func TestResult_InvalidWithoutEnvironment(t *testing.T) {
	e := completeEnv()
	e.PostgresVer = "unknown"
	r := &Result{Scenario: "s1", Source: "real run", Env: e}
	r.Finish(time.Unix(100, 0))
	if !r.Invalid {
		t.Fatal("incomplete record must be invalid")
	}
	if _, err := r.JSON(); err == nil || !strings.Contains(err.Error(), "postgres_version") {
		t.Errorf("JSON must refuse and name the field: %v", err)
	}
}

func TestResult_FormatRoundTrip(t *testing.T) {
	r := &Result{SchemaVersion: 1, Scenario: "s2", Source: "real run", Command: "mcbench -scenario s2", Env: completeEnv(),
		Series: []Series{NewSeries("x", "ms", "n", []float64{9}, []float64{1, 2, 3})}}
	r.Finish(time.Unix(100, 0))
	js, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Result
	if err := json.Unmarshal(js, &back); err != nil {
		t.Fatal(err)
	}
	s := back.Series[0]
	if back.Source != "real run" || s.Stats.Median != 2 || len(s.Warmup) != 1 || s.Stats.N != 3 {
		t.Errorf("round trip lost data: %+v", back)
	}
	md := r.Markdown()
	for _, want := range []string{"baseline of THIS machine", "not a performance claim", "| x | ms | 3 | 1.000 | 2.000 |", "real run"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
}

func TestRedact_RemovesCredentials(t *testing.T) {
	for in, want := range map[string]string{
		"-db-url=postgres://u:secret@127.0.0.1:5433/x -n 5": "-db-url=postgres://127.0.0.1:5433/x -n 5",
		"-db-url postgres://u:secret@127.0.0.1:5433/x -n 5": "-db-url postgres://127.0.0.1:5433/x -n 5",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeCoordinator succeeds every job immediately and hands out jobs to polls.
type fakeCoordinator struct {
	mu     sync.Mutex
	jobs   []string
	queued []string
}

func (f *fakeCoordinator) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := r.URL.Path
	switch {
	case r.Method == "POST" && p == "/v1/jobs":
		id := fmt.Sprintf("job%d", len(f.jobs)+1)
		f.jobs = append(f.jobs, id)
		f.queued = append(f.queued, id)
		w.WriteHeader(201)
		_, _ = fmt.Fprintf(w, `{"id":%q}`, id)
	case r.Method == "GET" && strings.HasSuffix(p, "/attempts"):
		now := time.Now().UTC()
		fin := now.Add(5 * time.Millisecond)
		b, _ := json.Marshal(map[string]any{"attempts": []map[string]any{{"worker_id": "w1", "state": "COMPLETED", "started_at": now, "finished_at": fin}}})
		_, _ = w.Write(b)
	case r.Method == "GET" && strings.HasPrefix(p, "/v1/jobs/"):
		_, _ = w.Write([]byte(`{"state":"SUCCEEDED"}`))
	case strings.HasSuffix(p, "/register"), strings.HasSuffix(p, "/heartbeat"), strings.HasSuffix(p, "/result"):
		_, _ = w.Write([]byte(`{}`))
	case strings.HasSuffix(p, "/poll"):
		if len(f.queued) == 0 {
			w.WriteHeader(204)
			return
		}
		id := f.queued[0]
		f.queued = f.queued[1:]
		_, _ = fmt.Fprintf(w, `{"assignment":{"job_id":%q,"attempt_id":"a","lease_token":"l"}}`, id)
	default:
		w.WriteHeader(404)
	}
}

func fakeAPI(t *testing.T) *API {
	srv := httptest.NewServer(&fakeCoordinator{})
	t.Cleanup(srv.Close)
	a := NewAPI(srv.URL)
	a.Sleep = func(time.Duration) {} // no real-clock sleeping in tests
	return a
}

func TestSmoke_AllScenariosAgainstFake(t *testing.T) {
	p := Params{M: 3, B: 3, N: 2, Polls: 4, Claims: 3, Warmup: 1, Repeats: 2, Task: "echo", Workers: 1, Iterations: 10, ProbeID: "probe"}
	for name, f := range map[string]func(*API, Params) ([]Series, []string, error){"s1": RunS1, "s2": RunS2, "s3": RunS3, "s4": RunS4} {
		series, _, err := f(fakeAPI(t), p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, s := range series {
			if name != "s2" && s.Stats.N == 0 {
				t.Errorf("%s/%s recorded nothing", name, s.Name)
			}
		}
	}
	// warm-up is discarded and recorded separately
	series, _, _ := RunS1(fakeAPI(t), p)
	if len(series[0].Warmup) != 1 || len(series[0].Values) != 2 {
		t.Errorf("warmup/values = %d/%d", len(series[0].Warmup), len(series[0].Values))
	}
}
