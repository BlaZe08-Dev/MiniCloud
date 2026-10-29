package abuse_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/config"
	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/executor"
	"github.com/BlaZe08-Dev/Minicloud/internal/worker"
	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// bigEchoTask replaces the built-in echo (the only task type the worker registers for besides sleep
// and sha256_loop) with one that misbehaves: it returns more output and error text than the
// documented limits. It exists only in this test file.
type bigEchoTask struct {
	output string
	err    error
}

func (bigEchoTask) Name() string            { return "echo" }
func (bigEchoTask) Validate(_ []byte) error { return nil }
func (b bigEchoTask) Execute(_ context.Context, _ []byte) (string, error) {
	return b.output, b.err
}

// runOversizeJob runs the real worker agent against the real coordinator until the job is terminal
// and logs a timeline. A lost attempt (no result stored) is expired by the test only after the
// attempt has stayed RUNNING for 1.2 s of real time, which a delivered result never does.
func runOversizeJob(t *testing.T, kind string, tasks []executor.Task, payload string) (*sectest.Rig, string, time.Duration) {
	t.Helper()
	logs := sectest.CaptureLogs(t)
	r := sectest.NewRig(t, kind, sectest.Options{Lease: 30 * time.Second, Tasks: tasks})
	job := r.SubmitTask("echo", payload, 3)

	reg := executor.NewRegistry()
	for _, task := range tasks {
		reg.Register(task)
	}
	agent := worker.NewAgent(&config.WorkerConfig{WorkerID: "w1", CoordinatorURL: r.Srv.URL, HeartbeatInterval: time.Second, TotalSlots: 1}, reg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = agent.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	start := time.Now()
	seen := map[string]time.Time{}
	last := ""
	for time.Since(start) < 25*time.Second {
		j := r.Job(job)
		snap := string(j.State)
		stuck := false
		for _, a := range r.Attempts(job) {
			snap += fmt.Sprintf(" a%d=%s", a.AttemptNo, a.State)
			if a.State == domain.AttemptRunning {
				first, ok := seen[a.ID]
				if !ok {
					seen[a.ID] = time.Now()
				} else if time.Since(first) > 1200*time.Millisecond {
					stuck = true
				}
			}
		}
		if snap != last {
			t.Logf("[%s] T+%.2fs job/attempts: %s", kind, time.Since(start).Seconds(), snap)
			last = snap
		}
		if j.State.IsTerminal() {
			break
		}
		if stuck {
			t.Logf("[%s] T+%.2fs attempt stuck RUNNING with no result stored; expiring its lease and sweeping", kind, time.Since(start).Seconds())
			r.Expire()
			r.MustSweep()
			r.MakeRetryDue()
		}
		time.Sleep(100 * time.Millisecond)
	}
	elapsed := time.Since(start)
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "rejected") || strings.Contains(line, "committed") || strings.Contains(line, "renew") {
			t.Logf("[%s] worker log: %.230s", kind, line)
		}
	}
	return r, job, elapsed
}

func requireClean(t *testing.T, kind string, r *sectest.Rig, job string, elapsed time.Duration, wantState domain.JobState) {
	t.Helper()
	j := r.Job(job)
	atts := r.Attempts(job)
	for _, a := range atts {
		t.Logf("[%s] attempt %d: %s class=%s exit=%d output=%d bytes error=%.100q", kind, a.AttemptNo, a.State, a.ErrorClass, a.ExitCode, len(a.Output), a.Error)
	}
	t.Logf("[%s] final: job %s after %.2fs, %d attempt(s), %d result row(s)", kind, j.State, elapsed.Seconds(), len(atts), r.ResultRows(job))
	if j.State != wantState {
		t.Fatalf("[%s] job ended %s, want %s", kind, j.State, wantState)
	}
	for _, a := range atts {
		if a.State == domain.AttemptLost {
			t.Errorf("[%s] attempt %d is LOST: the result never reached the coordinator", kind, a.AttemptNo)
		}
		if len(a.Output) > executor.MaxOutputSizeBytes || len(a.Error) > executor.MaxErrorSizeBytes {
			t.Errorf("[%s] attempt %d stored %d output / %d error bytes, over the limits", kind, a.AttemptNo, len(a.Output), len(a.Error))
		}
	}
}

// TestAbuse_OversizeOutput_BuiltinEchoMidRuneTruncation drives the built-in echo executor, with no
// test executor, through the real worker. The 65,535-byte payload makes the echo output 65,544 bytes
// (the wrapper adds 9) and ends in 3-byte runes, so the 64 KiB cut falls inside one of them. Cutting
// bytes leaves invalid UTF-8; the worker's JSON encoder turns each stray byte into U+FFFD (3 bytes),
// the text grows past 64 KiB and the coordinator answers 400 OUTPUT_TOO_LARGE. Required: the job
// ends SUCCEEDED with a stored output within the limit and a truncation marker, no attempt LOST.
func TestAbuse_OversizeOutput_BuiltinEchoMidRuneTruncation(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		payload := `{"m":"` + strings.Repeat("a", 65227) + strings.Repeat("€", 100) + `"}`
		r, job, elapsed := runOversizeJob(t, kind, nil, payload)
		requireClean(t, kind, r, job, elapsed, domain.StateSucceeded)
		out := r.Attempts(job)[0].Output
		if !strings.HasSuffix(out, executor.TruncationMarker) {
			t.Errorf("[%s] stored output does not end with the truncation marker: tail %q", kind, out[max(0, len(out)-40):])
		}
	})
}

// TestAbuse_OversizeOutput_ErrorPathUncapped uses a test executor that fails with 100 KiB of
// output and 40 KiB of error text. The built-in cap covers only the success path of
// Registry.Execute; the error path returned the output and the error text unbounded. Required: the
// job ends FAILED (a PERMANENT failure does not retry) with both fields truncated and marked.
func TestAbuse_OversizeOutput_ErrorPathUncapped(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		task := bigEchoTask{output: strings.Repeat("o", 100*1024), err: fmt.Errorf("%w: %s", domain.ErrInvalidRequest, strings.Repeat("e", 40*1024))}
		r, job, elapsed := runOversizeJob(t, kind, []executor.Task{task}, `{}`)
		requireClean(t, kind, r, job, elapsed, domain.StateFailed)
		a := r.Attempts(job)[0]
		if !strings.HasSuffix(a.Error, executor.TruncationMarker) || !strings.HasSuffix(a.Output, executor.TruncationMarker) {
			t.Errorf("[%s] stored error/output not marked as truncated", kind)
		}
	})
}

// TestAbuse_OversizeOutput_PlainASCII is the control: plain ASCII over the limit on the success path
// was already capped by Registry.Execute and must keep ending SUCCEEDED, now with a marker.
func TestAbuse_OversizeOutput_PlainASCII(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		task := bigEchoTask{output: strings.Repeat("a", 100*1024)}
		r, job, elapsed := runOversizeJob(t, kind, []executor.Task{task}, `{}`)
		requireClean(t, kind, r, job, elapsed, domain.StateSucceeded)
		if out := r.Attempts(job)[0].Output; len(out) != executor.MaxOutputSizeBytes || !strings.HasSuffix(out, executor.TruncationMarker) {
			t.Errorf("[%s] stored %d bytes, want exactly %d ending in the marker", kind, len(out), executor.MaxOutputSizeBytes)
		}
	})
}

// The four hostile-text cases of the P4-4e fix round 2, through the real worker.

// TestAbuse_WorkerResult_NULEscapeInEchoPayload is case 1 with the built-in echo: a payload holding
// the JSON escape for NUL. Echo decodes and re-encodes, so its output carries the six characters
// of the escape, never a raw NUL byte. Required: the job ends SUCCEEDED, output stored as is.
func TestAbuse_WorkerResult_NULEscapeInEchoPayload(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r, job, elapsed := runOversizeJob(t, kind, nil, `{"m":"a\u0000b"}`)
		requireClean(t, kind, r, job, elapsed, domain.StateSucceeded)
		if out := r.Attempts(job)[0].Output; strings.ContainsRune(out, 0) || !strings.Contains(out, `\u0000`) {
			t.Errorf("[%s] stored output %q, want the escape text and no raw NUL", kind, out)
		}
	})
}

// TestAbuse_WorkerResult_NULByteInOutput is case 1 with a task that returns a raw NUL byte (no
// built-in task can). The coordinator rejects NUL in output with 400 (F-4e-19). Required: the
// worker replaces the byte, the job ends SUCCEEDED and the stored output holds U+FFFD.
func TestAbuse_WorkerResult_NULByteInOutput(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r, job, elapsed := runOversizeJob(t, kind, []executor.Task{bigEchoTask{output: "a\x00b"}}, `{}`)
		requireClean(t, kind, r, job, elapsed, domain.StateSucceeded)
		if out := r.Attempts(job)[0].Output; out != "a�b" {
			t.Errorf("[%s] stored output %q, want %q", kind, out, "a�b")
		}
	})
}

// TestAbuse_WorkerResult_InvalidUTF8InOutput is case 2: no built-in task can return invalid UTF-8
// (sleep and sha256_loop build JSON with the encoder, echo re-encodes), so a test task does.
func TestAbuse_WorkerResult_InvalidUTF8InOutput(t *testing.T) {
	sectest.EachBackend(t, func(t *testing.T, kind string) {
		r, job, elapsed := runOversizeJob(t, kind, []executor.Task{bigEchoTask{output: "a\xff\xfeb"}}, `{}`)
		requireClean(t, kind, r, job, elapsed, domain.StateSucceeded)
		if out := r.Attempts(job)[0].Output; out != "a��b" {
			t.Errorf("[%s] stored output %q, want %q", kind, out, "a��b")
		}
	})
}

// TestAbuse_WorkerResult_ErrorTextHostile is case 4: a failing task whose error text is near, at
// and over 10 KiB, made of NUL bytes and invalid bytes. Required: FAILED after one attempt, stored
// error valid, free of NUL and within 10,240 bytes.
func TestAbuse_WorkerResult_ErrorTextHostile(t *testing.T) {
	for _, n := range []int{10*1024 - 100, 10 * 1024, 10*1024 + 1, 40 * 1024} {
		n := n
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			sectest.EachBackend(t, func(t *testing.T, kind string) {
				text := strings.Repeat("\x00\xffx", n/3+1)[:n]
				task := bigEchoTask{err: fmt.Errorf("%w: %s", domain.ErrInvalidRequest, text)}
				r, job, elapsed := runOversizeJob(t, kind, []executor.Task{task}, `{}`)
				requireClean(t, kind, r, job, elapsed, domain.StateFailed)
				e := r.Attempts(job)[0].Error
				if strings.ContainsRune(e, 0) || len(e) > executor.MaxErrorSizeBytes || !strings.HasPrefix(e, "invalid request") {
					t.Errorf("[%s] n=%d stored error of %d bytes unusable (NUL %v)", kind, n, len(e), strings.ContainsRune(e, 0))
				}
			})
		})
	}
}
