package sectest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/api"
	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage/postgres"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker/workertest"
)

const (
	KindMemory   = "memory"
	KindPostgres = "postgres"
)

// Kinds lists the backends to run: memory always, postgres only when the test database is configured.
func Kinds() []string {
	if DatabaseURL() == "" {
		return []string{KindMemory}
	}
	return []string{KindMemory, KindPostgres}
}

// EachBackend runs fn as a subtest per available backend.
func EachBackend(t *testing.T, fn func(t *testing.T, kind string)) {
	t.Helper()
	for _, k := range Kinds() {
		k := k
		t.Run(k, func(t *testing.T) { fn(t, k) })
	}
}

// Options configures a Rig. Zero values give a 30 s lease, no startup grace and an outage window
// equal to the lease.
type Options struct {
	Lease         time.Duration
	StartupGrace  time.Duration
	OutageWindow  time.Duration
	WorkerTimeout time.Duration
	PG            *PGSchema // reuse a schema instead of creating one
	PoolURL       string    // postgres only: connect through this URL (a fault proxy) instead of PG.SchemaURL
	PoolMaxConns  int32
	Tasks         []executor.Task
}

// Rig is an in-process coordinator (real api handler, real service, real repository) behind an
// httptest server. Memory rigs run on a FakeClock; PostgreSQL rigs run on database time.
type Rig struct {
	T       *testing.T
	Kind    string
	Lease   time.Duration
	Repo    storage.Repository
	Svc     *coordinator.Service
	Srv     *httptest.Server
	Clock   *workertest.FakeClock // memory only
	PG      *PGSchema             // postgres only
	Reg     *executor.Registry
	Sweeper *coordinator.LeaseSweeper
	Client  *http.Client
}

// NewRig builds a rig of the given kind.
func NewRig(t *testing.T, kind string, opts ...Options) *Rig {
	t.Helper()
	var o Options
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Lease == 0 {
		o.Lease = 30 * time.Second
	}
	if o.OutageWindow == 0 {
		o.OutageWindow = o.Lease
	}
	if o.WorkerTimeout == 0 {
		o.WorkerTimeout = time.Hour
	}
	r := &Rig{T: t, Kind: kind, Lease: o.Lease, Reg: executor.NewRegistry(), Client: &http.Client{Timeout: 30 * time.Second}}
	for _, task := range o.Tasks {
		r.Reg.Register(task)
	}
	switch kind {
	case KindMemory:
		r.Clock = workertest.NewFakeClock(time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC))
		r.Repo = storage.NewInMemoryRepository(storage.WithClock(r.Clock), storage.WithLeaseDuration(o.Lease))
		r.Svc = coordinator.NewService(r.Repo, scheduler.NewRoundRobinScheduler(), r.Reg, o.WorkerTimeout)
		r.Svc.SetClock(r.Clock)
	case KindPostgres:
		r.PG = o.PG
		if r.PG == nil {
			r.PG = NewPGSchema(t)
		}
		url := o.PoolURL
		if url == "" {
			url = r.PG.SchemaURL
		}
		maxc := o.PoolMaxConns
		if maxc == 0 {
			maxc = 25
		}
		pool, err := r.PG.OpenPool(context.Background(), url, maxc)
		if err != nil {
			t.Fatalf("open rig pool: %v", err)
		}
		t.Cleanup(pool.Close)
		r.Repo = postgres.NewRepository(pool, postgres.WithLeaseDuration(o.Lease))
		r.Svc = coordinator.NewPostgresService(r.Repo, scheduler.NewRoundRobinScheduler(), r.Reg, o.WorkerTimeout)
	default:
		t.Fatalf("unknown backend kind %q", kind)
	}
	r.Svc.SetLeaseDuration(o.Lease)
	r.Svc.SetGracePeriods(o.StartupGrace, o.OutageWindow)
	r.Svc.SetSweepRandom(func() float64 { return 0.5 })
	// The sweeper interval is also the base of the inter-tick gap threshold (3x). Using the lease
	// as the interval keeps a fake-clock jump of one lease from looking like an outage.
	r.Sweeper = r.Svc.NewLeaseSweeper(o.Lease)
	r.Srv = httptest.NewServer(api.NewServer("127.0.0.1:0", r.Svc).Handler())
	t.Cleanup(r.Srv.Close)
	return r
}

// Resp is one HTTP response.
type Resp struct {
	Status int
	Body   []byte
	Header http.Header
}

func (r Resp) String() string {
	return fmt.Sprintf("%d %s", r.Status, strings.TrimSpace(string(r.Body)))
}

// ErrCode returns error.code of an error envelope, or "".
func (r Resp) ErrCode() string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(r.Body, &e)
	return e.Error.Code
}

// Do sends a request. body may be nil, a string/[]byte (sent verbatim) or any value (JSON-encoded).
func (r *Rig) Do(method, path string, body any, hdr ...string) Resp {
	r.T.Helper()
	return DoURL(r.T, r.Client, r.Srv.URL, method, path, body, hdr...)
}

// DoURL is Do against an arbitrary base URL. hdr is key,value pairs.
func DoURL(t testing.TB, c *http.Client, base, method, path string, body any, hdr ...string) Resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, rd)
	if err != nil {
		t.Fatalf("new request %s %q: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return Resp{Status: resp.StatusCode, Body: raw, Header: resp.Header}
}

// Register registers a worker that supports the allowlisted tasks.
func (r *Rig) Register(id string, slots int) Resp {
	r.T.Helper()
	return r.Do("POST", "/v1/workers/register", map[string]any{
		"worker_id": id, "id": id, "version": "sec", "total_slots": slots,
		"supported_tasks": []string{"echo", "sleep", "sha256_loop"},
	})
}

// Submit submits an echo job and returns its id.
func (r *Rig) Submit(maxAttempts int) string {
	r.T.Helper()
	return r.SubmitTask("echo", `{"message":"x"}`, maxAttempts)
}

// SubmitTask submits a job of the given task type.
func (r *Rig) SubmitTask(task, payload string, maxAttempts int) string {
	r.T.Helper()
	resp := r.Do("POST", "/v1/jobs", fmt.Sprintf(`{"task_type":%q,"payload":%s,"max_attempts":%d}`, task, payload, maxAttempts))
	if resp.Status != http.StatusCreated {
		r.T.Fatalf("submit: %s", resp)
	}
	var j struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(resp.Body, &j)
	return j.ID
}

// Poll claims work for a worker; it returns nil on 204.
func (r *Rig) Poll(workerID string) *coordinator.WorkAssignment {
	r.T.Helper()
	resp := r.Do("POST", "/v1/workers/"+workerID+"/poll", "")
	switch resp.Status {
	case http.StatusNoContent:
		return nil
	case http.StatusOK:
		var env struct {
			Assignment coordinator.WorkAssignment `json:"assignment"`
		}
		if err := json.Unmarshal(resp.Body, &env); err != nil {
			r.T.Fatalf("poll decode: %v", err)
		}
		return &env.Assignment
	default:
		r.T.Fatalf("poll %s: %s", workerID, resp)
		return nil
	}
}

// Result posts a result.
func (r *Rig) Result(jobID, attemptID, token, workerID string, exit int, errStr, class string) Resp {
	r.T.Helper()
	m := map[string]any{"lease_token": token, "worker_id": workerID, "exit_code": exit, "output": "o", "error": errStr}
	if class != "" {
		m["error_classification"] = class
	}
	return r.Do("POST", "/v1/jobs/"+jobID+"/attempts/"+attemptID+"/result", m)
}

// Renew posts a renewal.
func (r *Rig) Renew(jobID, attemptID, token, workerID string) Resp {
	r.T.Helper()
	return r.Do("POST", "/v1/jobs/"+jobID+"/attempts/"+attemptID+"/renew", map[string]string{"lease_token": token, "worker_id": workerID})
}

// Cancel posts a cancel.
func (r *Rig) Cancel(jobID string) Resp {
	r.T.Helper()
	return r.Do("POST", "/v1/jobs/"+jobID+"/cancel", "")
}

// Job reads the stored job through the repository.
func (r *Rig) Job(jobID string) *domain.Job {
	r.T.Helper()
	j, err := r.Repo.GetJob(context.Background(), jobID)
	if err != nil {
		r.T.Fatalf("get job %s: %v", jobID, err)
	}
	return j
}

// Attempts reads the stored attempts of a job.
func (r *Rig) Attempts(jobID string) []*domain.Attempt {
	r.T.Helper()
	a, err := r.Repo.ListAttemptsByJob(context.Background(), jobID)
	if err != nil {
		r.T.Fatalf("list attempts %s: %v", jobID, err)
	}
	return a
}

// Events reads the stored audit events of a job.
func (r *Rig) Events(jobID string) []*domain.JobEvent {
	r.T.Helper()
	e, err := r.Repo.ListEventsByJob(context.Background(), jobID)
	if err != nil {
		r.T.Fatalf("list events %s: %v", jobID, err)
	}
	return e
}

// CountEvents counts a job's events of one type.
func (r *Rig) CountEvents(jobID string, typ domain.JobEventType) int {
	n := 0
	for _, e := range r.Events(jobID) {
		if e.EventType == typ {
			n++
		}
	}
	return n
}

// ResultRows is the number of stored result rows of a job.
func (r *Rig) ResultRows(jobID string) int {
	r.T.Helper()
	if r.PG != nil {
		return r.PG.Count(r.T, "SELECT count(*) FROM results WHERE job_id = $1", jobID)
	}
	if r.Job(jobID).Result != nil {
		return 1
	}
	return 0
}

// Expire makes every RUNNING attempt's lease lapse. Memory: the shared fake clock jumps past the
// lease. PostgreSQL: lease_expires_at is moved into the past (a test-side action on the isolated
// schema; the repository still reads database time).
func (r *Rig) Expire() {
	r.T.Helper()
	if r.Clock != nil {
		r.Clock.Advance(r.Lease + time.Second)
		return
	}
	r.PG.Exec(r.T, "UPDATE attempts SET lease_expires_at = clock_timestamp() - interval '1 second' WHERE state = 'RUNNING'")
}

// MakeRetryDue makes every RETRYING job's backoff elapse (memory: fake clock jump; postgres: not_before moved).
func (r *Rig) MakeRetryDue() {
	r.T.Helper()
	if r.Clock != nil {
		r.Clock.Advance(time.Minute)
		return
	}
	r.PG.Exec(r.T, "UPDATE jobs SET not_before = clock_timestamp() - interval '1 second' WHERE state = 'RETRYING'")
}

// Sweep runs one sweeper tick and returns its error.
func (r *Rig) Sweep() error {
	return r.Sweeper.Tick(context.Background())
}

// MustSweep runs one tick and fails the test on error.
func (r *Rig) MustSweep() {
	r.T.Helper()
	if err := r.Sweep(); err != nil {
		r.T.Fatalf("sweeper tick: %v", err)
	}
}

// SafeBuffer is a goroutine-safe byte buffer for captured logs and process output.
type SafeBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *SafeBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *SafeBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// CaptureLogs routes slog.Default into a buffer for the test and restores it afterwards. Tests that
// use it must not run in parallel with each other.
func CaptureLogs(t testing.TB) *SafeBuffer {
	t.Helper()
	buf := &SafeBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}
