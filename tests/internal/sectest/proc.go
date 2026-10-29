package sectest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error

	pidMu    sync.Mutex
	started  []int
	portLock []*os.File
)

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "..")
}

// Binaries builds coordinator, worker and mc once into a private temporary directory and returns it.
func Binaries(t testing.TB) string {
	t.Helper()
	buildOnce.Do(func() {
		binDir, buildErr = os.MkdirTemp("", "minicloud-sectest-bin-")
		if buildErr != nil {
			return
		}
		for _, name := range []string{"coordinator", "worker", "mc"} {
			cmd := exec.Command("go", "build", "-o", filepath.Join(binDir, name), "./cmd/"+name)
			cmd.Dir = repoRoot()
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", name, err, out)
				return
			}
		}
	})
	if buildErr != nil {
		t.Fatalf("%v", buildErr)
	}
	return binDir
}

// Cleanup removes the build directory and reports any started process that is still alive. Call it
// from TestMain after m.Run; it returns the number of leftover processes.
func Cleanup() int {
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	left := 0
	pidMu.Lock()
	defer pidMu.Unlock()
	for _, pid := range started {
		if syscall.Kill(pid, 0) == nil {
			fmt.Fprintf(os.Stderr, "LEFTOVER PROCESS pid %d\n", pid)
			_ = syscall.Kill(pid, syscall.SIGKILL) // recorded PID only
			left++
		}
	}
	for _, f := range portLock {
		_ = f.Close()
	}
	return left
}

// FreePort returns a free 127.0.0.1 address from port 18080 upward. Candidates are claimed with an
// flock on a file in the temp directory so concurrently running test binaries never pick the same
// port; the claim is held until the process exits.
func FreePort(t testing.TB) string {
	t.Helper()
	for port := 18080; port < 18080+2000; port++ {
		f, err := os.OpenFile(filepath.Join(os.TempDir(), fmt.Sprintf("minicloud-sectest-port-%d.lock", port)), os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			_ = f.Close()
			continue
		}
		addr := fmt.Sprintf("127.0.0.1:%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			_ = f.Close()
			continue
		}
		_ = ln.Close()
		pidMu.Lock()
		portLock = append(portLock, f)
		pidMu.Unlock()
		return addr
	}
	t.Fatal("no free port from 18080")
	return ""
}

// Proc is one real child process started by a test.
type Proc struct {
	Name string
	PID  int
	Out  *SafeBuffer
	cmd  *exec.Cmd
	done chan struct{}
}

// StartProc starts bin with exactly env (nothing is inherited) and kills it on cleanup.
func StartProc(t testing.TB, name, bin string, env []string) *Proc {
	t.Helper()
	p := &Proc{Name: name, Out: &SafeBuffer{}, done: make(chan struct{})}
	p.cmd = exec.Command(bin)
	p.cmd.Env = append([]string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME")}, env...)
	p.cmd.Stdout = p.Out
	p.cmd.Stderr = p.Out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	p.PID = p.cmd.Process.Pid
	pidMu.Lock()
	started = append(started, p.PID)
	pidMu.Unlock()
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() { p.Kill9() })
	return p
}

// Kill9 sends SIGKILL to this process only and waits for it to be reaped.
func (p *Proc) Kill9() {
	select {
	case <-p.done:
		return
	default:
	}
	_ = p.cmd.Process.Signal(syscall.SIGKILL)
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
	}
}

// Alive reports whether the process has not exited.
func (p *Proc) Alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// CoordinatorEnv returns the environment of a PostgreSQL-backed coordinator on addr.
func CoordinatorEnv(addr, dbURL string, lease, startupGrace time.Duration) []string {
	return []string{
		"MINICLOUD_HTTP_ADDR=" + addr,
		"MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL=" + dbURL,
		"MINICLOUD_HEARTBEAT_INTERVAL=1s",
		"MINICLOUD_WORKER_TIMEOUT=5s",
		"MINICLOUD_REAPER_INTERVAL=1s",
		"MINICLOUD_LEASE_DURATION=" + lease.String(),
		"MINICLOUD_STARTUP_GRACE_PERIOD=" + startupGrace.String(),
		"MINICLOUD_OUTAGE_GRACE_WINDOW=" + lease.String(),
		"MINICLOUD_SWEEPER_INTERVAL=500ms",
		"MINICLOUD_DEFAULT_MAX_ATTEMPTS=5",
	}
}

// WorkerEnv returns the environment of a worker process.
func WorkerEnv(id, coordinatorURL string, slots int) []string {
	return []string{
		"MINICLOUD_COORDINATOR_URL=" + coordinatorURL,
		"MINICLOUD_WORKER_ID=" + id,
		fmt.Sprintf("MINICLOUD_WORKER_SLOTS=%d", slots),
		"MINICLOUD_HEARTBEAT_INTERVAL=300ms",
	}
}

// WaitFor polls cond every 50 ms until it is true or the deadline passes; it reports success.
func WaitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond) // bounded poll interval
	}
}

// Term sends SIGTERM to this process only.
func (p *Proc) Term() { _ = p.cmd.Process.Signal(syscall.SIGTERM) }

// WaitExit waits up to timeout for the process to exit and reports whether it did.
func (p *Proc) WaitExit(timeout time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(timeout):
		return false
	}
}
