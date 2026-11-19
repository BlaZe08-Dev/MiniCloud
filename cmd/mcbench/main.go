package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "mcbench:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("mcbench", flag.ContinueOnError)
	scenario := fs.String("scenario", "s1", "s1, s2, s3, s4")
	dbURL := fs.String("db-url", os.Getenv("MINICLOUD_TEST_DATABASE_URL"), "own database URL (127.0.0.1:5433 only)")
	allowedDB := fs.String("allowed-db", "", "the only database name this run may use (required)")
	start := fs.Bool("start", true, "start coordinator and workers from -bin-dir; false targets -coordinator-url")
	coordURL := fs.String("coordinator-url", "http://127.0.0.1:18080", "coordinator URL (loopback only)")
	binDir := fs.String("bin-dir", "bin", "directory with coordinator and worker binaries (make build)")
	policy := fs.String("policy", "round_robin", "round_robin or fifo_pull")
	slots := fs.Int("slots", 4, "slots per worker")
	workers := fs.Int("workers", 2, "worker count")
	m := fs.Int("m", 40, "S1 jobs per run")
	b := fs.Int("b", 40, "S4 burst size")
	n := fs.Int("n", 5, "S3 repeats per worker (placeholder, not the V2-3 value, OD-22)")
	polls := fs.Int("polls", 200, "S2 idle polls (placeholder)")
	claims := fs.Int("claims", 200, "S2 claiming polls (placeholder)")
	warmup := fs.Int("warmup", 10, "S2 discarded warm-up measurements")
	repeats := fs.Int("repeats", 3, "S1/S4 recorded repeats after one discarded warm-up run")
	task := fs.String("task", "sleep", "S1/S4 task: echo, sleep, sha256_loop")
	sleepMs := fs.Int("sleep-ms", 200, "sleep task duration")
	iters := fs.Int("iterations", 200000, "sha256_loop iterations (S3 and sha256 S1/S4)")
	maxLoad := fs.Float64("max-load", 1.0, "abort when the 1-minute load average exceeds this")
	out := fs.String("out", "", "output directory (default: a new temporary directory)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := CheckDatabaseURL(*dbURL, *allowedDB); err != nil {
		return err
	}
	if err := CheckLoopbackURL(*coordURL); err != nil {
		return err
	}
	if *policy != "round_robin" && *policy != "fifo_pull" {
		return fmt.Errorf("--policy must be round_robin or fifo_pull")
	}
	if *scenario == "s2" {
		*workers = 0
	}
	before, err := CheckIdle(readLoad, *maxLoad)
	if err != nil {
		return err
	}
	env := collectEnv(*dbURL)
	env.Policy, env.Slots, env.Workers, env.N = *policy, *slots, *workers, *n
	env.PolicySource = "set by mcbench on the coordinator it started"
	if !*start {
		env.PolicySource = "declared by --policy; not verified on the targeted coordinator"
	}
	env.LoadBefore, env.MaxLoad, env.IdleCheck = before, *maxLoad, "passed"
	env.StartedAt = time.Now().UTC().Format(time.RFC3339)

	runID := fmt.Sprintf("mcbench%d", time.Now().Unix())
	if *start {
		stop, err := startCluster(*binDir, *coordURL, *dbURL, *policy, *slots, *workers, runID)
		if stop != nil {
			defer stop()
		}
		if err != nil {
			return err
		}
	}
	api := NewAPI(*coordURL)
	p := Params{M: *m, B: *b, N: *n, Polls: *polls, Claims: *claims, Warmup: *warmup, Repeats: *repeats,
		Task: *task, SleepMs: *sleepMs, Iterations: *iters, Workers: *workers, ProbeID: runID + "-probe"}
	if *scenario == "s3" {
		p.Task = "sha256_loop"
	}
	var series []Series
	var notes []string
	switch *scenario {
	case "s1":
		series, notes, err = RunS1(api, p)
	case "s2":
		series, notes, err = RunS2(api, p)
	case "s3":
		series, notes, err = RunS3(api, p)
	case "s4":
		series, notes, err = RunS4(api, p)
	default:
		err = fmt.Errorf("unknown scenario %q", *scenario)
	}
	if err != nil {
		return err
	}
	after, aerr := ParseLoadAvg(func() string { s, _ := readLoad(); return s }())
	if aerr != nil {
		after = LoadAvg{-1, -1, -1}
	}
	env.LoadAfter = after
	res := &Result{SchemaVersion: 1, Scenario: *scenario, Source: "real run", Command: "mcbench " + redact(strings.Join(args, " ")),
		Params: map[string]any{"policy": *policy, "slots": *slots, "workers": *workers, "m": *m, "b": *b, "n": *n, "polls": *polls,
			"claims": *claims, "warmup": *warmup, "repeats": *repeats, "task": p.Task, "sleep_ms": *sleepMs, "iterations": *iters},
		Env: env, Series: series, Notes: notes}
	res.Finish(time.Now())
	dir := *out
	if dir == "" {
		dir, _ = os.MkdirTemp("", "mcbench-")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	js, err := res.JSON()
	if err != nil {
		return err
	}
	base := fmt.Sprintf("%s-%s-%.7s", *scenario, time.Now().UTC().Format("20060102"), env.Commit)
	if err := os.WriteFile(filepath.Join(dir, base+".json"), js, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, base+".md"), []byte(res.Markdown()), 0o644); err != nil {
		return err
	}
	fmt.Print(res.Markdown())
	fmt.Println("written:", filepath.Join(dir, base+".json"))
	return nil
}

func redact(s string) string {
	fields := strings.Fields(s)
	for i, f := range fields {
		if strings.Contains(f, "://") && strings.Contains(f, "@") {
			if u, err := url.Parse(strings.TrimPrefix(f, "-db-url=")); err == nil {
				u.User = nil
				fields[i] = "-db-url=" + u.String()
			}
		}
	}
	return strings.Join(fields, " ")
}

// startCluster starts one coordinator and the workers; it returns a stop function.
func startCluster(binDir, coordURL, dbURL, policy string, slots, workers int, runID string) (func(), error) {
	u, _ := url.Parse(coordURL)
	var procs []*exec.Cmd
	stop := func() {
		for i := len(procs) - 1; i >= 0; i-- {
			_ = procs[i].Process.Signal(syscall.SIGTERM)
		}
		for _, c := range procs {
			done := make(chan struct{})
			go func(c *exec.Cmd) { _ = c.Wait(); close(done) }(c)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = c.Process.Kill()
			}
		}
	}
	coord := exec.Command(filepath.Join(binDir, "coordinator"))
	coord.Env = append(os.Environ(), "MINICLOUD_HTTP_ADDR="+u.Host, "MINICLOUD_STORAGE_DRIVER=postgres",
		"MINICLOUD_DATABASE_URL="+dbURL, "MINICLOUD_DISPATCH_POLICY="+policy)
	if err := coord.Start(); err != nil {
		return nil, err
	}
	procs = append(procs, coord)
	exited := make(chan struct{})
	go func() { _ = coord.Wait(); close(exited) }()
	api := NewAPI(coordURL)
	ready := false
	for i := 0; i < 150 && !ready; i++ {
		select {
		case <-exited:
			procs = nil
			return nil, fmt.Errorf("coordinator exited at startup; if the database is not migrated, run make db-up / the migrator for your own database first")
		default:
		}
		if code, _, _, err := api.do("GET", "/readyz", nil); err == nil && code == 200 {
			ready = true
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !ready {
		return stop, fmt.Errorf("coordinator not ready in 15s")
	}
	for i := 0; i < workers; i++ {
		w := exec.Command(filepath.Join(binDir, "worker"))
		w.Env = append(os.Environ(), "MINICLOUD_COORDINATOR_URL="+coordURL,
			fmt.Sprintf("MINICLOUD_WORKER_ID=%s-w%d", runID, i+1), fmt.Sprintf("MINICLOUD_WORKER_SLOTS=%d", slots))
		if err := w.Start(); err != nil {
			return stop, err
		}
		procs = append(procs, w)
	}
	for i := 0; i < 150; i++ {
		if c, err := api.WorkerCount(runID); err == nil && c >= workers {
			return stop, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return stop, fmt.Errorf("workers did not register in 15s")
}
