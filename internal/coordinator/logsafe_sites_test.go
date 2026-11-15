package coordinator_test

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// SEC-066 (P4-22): the three coordinator loop sites that log a repository error. The injected
// error has the exact text shape of a pgx v5 connect error (pgconn.ConnectError) and wraps the
// same net.OpError and errno; it is stdlib-only because internal/coordinator must not import pgx.

var leakSecrets = []string{"leakuser", "leakdb_test", "10.9.8.7", "6543", "db.leak.internal", "failed to connect", "dial"}

type leakConnectError struct{}

func (leakConnectError) Error() string {
	return "failed to connect to `user=leakuser database=leakdb_test`: 10.9.8.7:6543 (db.leak.internal): " +
		"dial error: dial tcp 10.9.8.7:6543: connect: connection refused"
}

func (leakConnectError) Unwrap() error {
	return &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.9.8.7"), Port: 6543},
		Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
}

var errLeakConnect error = leakConnectError{}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureJSONLogs routes slog.Default through the JSON handler the coordinator binary uses.
func captureJSONLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// requireClassOnly fails unless a line with msg exists, every such line carries the class label,
// and the whole log holds none of the secrets of the injected error.
func requireClassOnly(t *testing.T, logs, msg string) {
	t.Helper()
	var lines []string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, `"msg":"`+msg+`"`) {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		t.Fatalf("positive control: no %q line was logged; logs:\n%s", msg, logs)
	}
	for _, l := range lines {
		if !strings.Contains(l, `"err":"connection_refused"`) {
			t.Fatalf("%q line does not carry the class label: %s", msg, l)
		}
	}
	for _, s := range leakSecrets {
		if strings.Contains(logs, s) {
			t.Fatalf("SECURITY LEAK: logs contain %q:\n%s", s, logs)
		}
	}
}

// leakPassRepo makes every assignment pass fail with the connect error.
type leakPassRepo struct {
	*storage.InMemoryRepository
}

func (leakPassRepo) AssignQueuedJobs(context.Context, storage.AssignParams) (storage.AssignResult, error) {
	return storage.AssignResult{}, errLeakConnect
}

// assignment_loop.go "assignment pass failed".
func TestLogSafe_AssignmentPassFailed_LogsClassOnly(t *testing.T) {
	logs := captureJSONLogs(t)
	clk := newMockClock(loopT0)
	repo := leakPassRepo{storage.NewInMemoryRepository(storage.WithClock(clk))}
	svc := loopService(t, repo, clk, coordinator.DefaultAssignmentSettings())
	if n := svc.NewAssignmentLoop().Round(context.Background()); n != 1 {
		t.Fatalf("a failed pass must end the round after 1 pass, ran %d", n)
	}
	requireClassOnly(t, logs.String(), "assignment pass failed")
}

// assignment.go "assignment pass refresh failed": a poll with a stale pass heartbeat runs one
// pass itself (ADR-0017 Section 3.2), and that pass fails.
func TestLogSafe_AssignmentPassRefreshFailed_LogsClassOnly(t *testing.T) {
	logs := captureJSONLogs(t)
	clk := newMockClock(loopT0)
	repo := leakPassRepo{storage.NewInMemoryRepository(storage.WithClock(clk))}
	svc := newRRService(t, repo, clk)
	rrHeartbeat(t, svc, "w_1", "echo")
	rrSubmit(t, svc, "echo")
	_, _ = svc.PollWork(context.Background(), "w_1")
	requireClassOnly(t, logs.String(), "assignment pass refresh failed")
}

// sweeper.go "lease sweeper tick failed". The tick still returns the original error (the grace
// state counts it as a failed tick): only the log text changed.
func TestLogSafe_LeaseSweeperTickFailed_LogsClassOnly(t *testing.T) {
	logs := captureJSONLogs(t)
	repo := newSweepFakeRepo()
	repo.set(sweepBase, errLeakConnect, nil, domain.SweepResult{})
	svc := coordinator.NewService(repo, scheduler.NewRoundRobinScheduler(), executor.NewRegistry(), 10*time.Second)
	svc.SetGracePeriods(0, sweepWindow)

	if err := svc.NewLeaseSweeper(sweepInterval).Tick(context.Background()); err != errLeakConnect {
		t.Fatalf("Tick returned %v, want the repository error unchanged", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.StartLeaseSweeper(ctx, 2*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(logs.String(), "lease sweeper tick failed") && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	requireClassOnly(t, logs.String(), "lease sweeper tick failed")
}
