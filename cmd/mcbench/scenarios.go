package main

import (
	"fmt"
	"sync"
	"time"
)

// Params are the scenario parameters shared across scenarios.
type Params struct {
	M, B, N, Polls, Claims, Warmup, Repeats int
	Task                                    string
	SleepMs, Iterations                     int
	Workers                                 int
	ProbeID                                 string
}

func (p Params) payload() any {
	switch p.Task {
	case "sleep":
		return map[string]any{"duration_ms": p.SleepMs}
	case "sha256_loop":
		return map[string]any{"iterations": p.Iterations, "input": "mcbench"}
	}
	return map[string]any{"message": "mcbench"}
}

func ms(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func (p Params) submitN(a *API, n int) ([]string, error) {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id, _, err := a.Submit(p.Task, p.payload())
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// RunS1 measures throughput: Repeats recorded runs after one discarded warm-up run.
func RunS1(a *API, p Params) ([]Series, []string, error) {
	var wall, jps, maxc, warmW, warmJ, warmC []float64
	for i := 0; i <= p.Repeats; i++ {
		t0 := a.Now()
		ids, err := p.submitN(a, p.M)
		if err != nil {
			return nil, nil, err
		}
		last, mc, err := a.WaitAll(ids, 10*time.Minute)
		if err != nil {
			return nil, nil, err
		}
		w := last.Sub(t0)
		j := float64(p.M) / w.Seconds()
		if i == 0 {
			warmW, warmJ, warmC = append(warmW, ms(w)), append(warmJ, j), append(warmC, float64(mc))
			continue
		}
		wall, jps, maxc = append(wall, ms(w)), append(jps, j), append(maxc, float64(mc))
	}
	return []Series{
		NewSeries("s1_wall_time", "ms", "first submit to last job seen SUCCEEDED, per repeat", warmW, wall),
		NewSeries("s1_jobs_per_second", "jobs/s", "M / wall time", warmJ, jps),
		NewSeries("s1_max_concurrent_attempts", "jobs", "highest RUNNING count seen in one sweep; a lower bound of the true peak", warmC, maxc),
	}, nil, nil
}

// RunS2 measures claim cost with a probe worker registered over HTTP. No other worker runs.
func RunS2(a *API, p Params) ([]Series, []string, error) {
	if err := a.Register(p.ProbeID, 8); err != nil {
		return nil, nil, err
	}
	var idle, idleW, sub, subW, claim, claimW, wait []float64
	// Heartbeat by elapsed time, not by poll count: the coordinator marks a probe worker
	// unhealthy after its worker timeout, and a loaded machine slows the polls.
	lastHB := time.Now()
	beat := func() {
		if time.Since(lastHB) >= time.Second {
			_ = a.Heartbeat(p.ProbeID)
			lastHB = time.Now()
		}
	}
	for i := 0; i < p.Polls+p.Warmup; i++ {
		beat()
		as, code, d, err := a.Poll(p.ProbeID)
		if err != nil {
			return nil, nil, err
		}
		if as != nil || code != 204 {
			return nil, nil, fmt.Errorf("idle poll returned a job: the database has leftover queued jobs; use a clean own database")
		}
		if i < p.Warmup {
			idleW = append(idleW, ms(d))
		} else {
			idle = append(idle, ms(d))
		}
	}
	const batch = 8
	done, seen := 0, 0
	for done < p.Claims+p.Warmup {
		n := batch
		if left := p.Claims + p.Warmup - done; left < n {
			n = left
		}
		for i := 0; i < n; i++ {
			_, d, err := a.Submit("echo", map[string]any{"message": "mcbench"})
			if err != nil {
				return nil, nil, err
			}
			if seen < p.Warmup {
				subW = append(subW, ms(d))
			} else {
				sub = append(sub, ms(d))
			}
			seen++
		}
		got := 0
		for tries := 0; got < n; tries++ {
			if tries > 20000 {
				return nil, nil, fmt.Errorf("claim timeout")
			}
			beat()
			as, code, d, err := a.Poll(p.ProbeID)
			if err != nil {
				return nil, nil, err
			}
			if as == nil {
				wait = append(wait, ms(d))
				_ = code
				a.Sleep(20 * time.Millisecond)
				continue
			}
			if done+got < p.Warmup {
				claimW = append(claimW, ms(d))
			} else {
				claim = append(claim, ms(d))
			}
			if err := a.Report(p.ProbeID, as); err != nil {
				return nil, nil, err
			}
			got++
		}
		done += n
	}
	return []Series{
		NewSeries("s2_poll_idle", "ms", "PollWork returning 204 with nothing queued; client-observed HTTP latency from 127.0.0.1, includes the database path", idleW, idle),
		NewSeries("s2_poll_claim", "ms", "PollWork returning 200 with a job; client-observed HTTP latency", claimW, claim),
		NewSeries("s2_poll_204_while_job_queued", "ms", "polls that returned 204 while a submitted job awaited reservation (round_robin) or were retried; recorded, not a headline", nil, wait),
		NewSeries("s2_submit", "ms", "POST /v1/jobs latency", subW, sub),
	}, []string{"SEC-037 recorded 20 database flights per claiming poll, 4 per empty poll, 5 per submission; these are end-to-end latencies, no per-flight cost was measured."}, nil
}

// RunS3 measures worker speed with sha256_loop, one job at a time on an otherwise idle
// cluster. Duration is attempt finished_at minus started_at (one coordinator clock):
// claim to commit, includes result delivery.
func RunS3(a *API, p Params) ([]Series, []string, error) {
	byWorker := map[string][]float64{}
	warm := map[string][]float64{}
	need := p.N + 1 // the first run on each worker is its discarded warm-up
	cap := p.Workers * need * 4
	for i := 0; i < cap; i++ {
		done := true
		for _, w := range workerNames(byWorker, warm) {
			if len(byWorker[w]) < p.N {
				done = false
			}
		}
		if done && len(byWorker) >= p.Workers {
			break
		}
		id, _, err := a.Submit("sha256_loop", p.payload())
		if err != nil {
			return nil, nil, err
		}
		if _, _, err := a.WaitAll([]string{id}, 5*time.Minute); err != nil {
			return nil, nil, err
		}
		at, _, err := a.Attempts(id)
		if err != nil || len(at) == 0 || at[len(at)-1].FinishedAt == nil {
			return nil, nil, fmt.Errorf("attempt timestamps missing")
		}
		x := at[len(at)-1]
		d := ms(x.FinishedAt.Sub(x.StartedAt))
		if len(warm[x.WorkerID]) == 0 {
			warm[x.WorkerID] = append(warm[x.WorkerID], d)
			continue
		}
		if len(byWorker[x.WorkerID]) < p.N {
			byWorker[x.WorkerID] = append(byWorker[x.WorkerID], d)
		}
	}
	var out []Series
	var notes []string
	for _, w := range workerNames(byWorker, warm) {
		out = append(out, NewSeries("s3_sha256_loop_"+w, "ms", fmt.Sprintf("iterations=%d; attempt finished_at minus started_at: claim to commit, includes result delivery", p.Iterations), warm[w], byWorker[w]))
		if len(byWorker[w]) < p.N {
			notes = append(notes, fmt.Sprintf("worker %s collected %d of N=%d runs before the submission cap", w, len(byWorker[w]), p.N))
		}
	}
	return out, notes, nil
}

func workerNames(m, w map[string][]float64) []string {
	set := map[string]bool{}
	for k := range m {
		set[k] = true
	}
	for k := range w {
		set[k] = true
	}
	var s []string
	for k := range set {
		s = append(s, k)
	}
	sortStrings(s)
	return s
}

// RunS4 submits B jobs at once into an idle cluster. Time to first claim is the earliest
// attempt started_at minus the moment the first submit began (one host clock).
func RunS4(a *API, p Params) ([]Series, []string, error) {
	var first, drain, warmF, warmD []float64
	for i := 0; i <= p.Repeats; i++ {
		ids := make([]string, p.B)
		errs := make([]error, p.B)
		var wg sync.WaitGroup
		t0 := a.Now()
		for k := 0; k < p.B; k++ {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()
				ids[k], _, errs[k] = a.Submit(p.Task, p.payload())
			}(k)
		}
		wg.Wait()
		for _, e := range errs {
			if e != nil {
				return nil, nil, e
			}
		}
		last, _, err := a.WaitAll(ids, 10*time.Minute)
		if err != nil {
			return nil, nil, err
		}
		var earliest time.Time
		for _, id := range ids {
			at, _, err := a.Attempts(id)
			if err != nil || len(at) == 0 {
				return nil, nil, fmt.Errorf("attempts missing for %s", id)
			}
			if earliest.IsZero() || at[0].StartedAt.Before(earliest) {
				earliest = at[0].StartedAt
			}
		}
		f, d := earliest.Sub(t0), last.Sub(t0)
		if i == 0 {
			warmF, warmD = append(warmF, ms(f)), append(warmD, ms(d))
			continue
		}
		first, drain = append(first, ms(f)), append(drain, ms(d))
	}
	return []Series{
		NewSeries("s4_time_to_first_claim", "ms", "earliest attempt started_at minus start of the first submit; one host clock", warmF, first),
		NewSeries("s4_time_to_drain", "ms", "start of the first submit to the last job seen SUCCEEDED", warmD, drain),
	}, nil, nil
}
