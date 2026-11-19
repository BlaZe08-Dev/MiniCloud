package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Environment is the mandatory record of ADR-0021 section 4. A result without a complete
// record is invalid.
type Environment struct {
	Commit       string   `json:"commit"`
	Dirty        bool     `json:"dirty"`
	GoVersion    string   `json:"go_version"`
	OS           string   `json:"os"`
	Kernel       string   `json:"kernel"`
	CPUModel     string   `json:"cpu_model"`
	CPUCores     int      `json:"cpu_cores"`
	RAMTotalKB   int64    `json:"ram_total_kb"`
	PostgresVer  string   `json:"postgres_version"`
	Policy       string   `json:"dispatch_policy"`
	PolicySource string   `json:"policy_source"`
	Slots        int      `json:"slots_per_worker"`
	Workers      int      `json:"worker_count"`
	N            int      `json:"n"`
	LoadBefore   LoadAvg  `json:"load_before"`
	LoadAfter    LoadAvg  `json:"load_after"`
	StartedAt    string   `json:"start_time"`
	EndedAt      string   `json:"end_time"`
	IdleCheck    string   `json:"idle_check"`
	MaxLoad      float64  `json:"max_load"`
	Missing      []string `json:"missing,omitempty"`
}

// Validate lists every field that is empty or unknown.
func (e *Environment) Validate() []string {
	var m []string
	chk := func(name, v string) {
		if v == "" || v == "unknown" {
			m = append(m, name)
		}
	}
	chk("commit", e.Commit)
	chk("go_version", e.GoVersion)
	chk("os", e.OS)
	chk("kernel", e.Kernel)
	chk("cpu_model", e.CPUModel)
	chk("postgres_version", e.PostgresVer)
	chk("dispatch_policy", e.Policy)
	chk("start_time", e.StartedAt)
	chk("end_time", e.EndedAt)
	if e.CPUCores <= 0 {
		m = append(m, "cpu_cores")
	}
	if e.RAMTotalKB <= 0 {
		m = append(m, "ram_total_kb")
	}
	if e.Workers < 0 || e.Slots <= 0 {
		m = append(m, "worker_count/slots")
	}
	if e.LoadBefore.One < 0 || e.LoadAfter.One < 0 {
		m = append(m, "load_average")
	}
	return m
}

// Series is one measured quantity: warm-up values (labelled, excluded from stats) and every
// recorded value.
type Series struct {
	Name   string    `json:"name"`
	Unit   string    `json:"unit"`
	Note   string    `json:"note,omitempty"`
	Warmup []float64 `json:"warmup_discarded"`
	Values []float64 `json:"values"`
	Stats  Stats     `json:"stats"`
}

// NewSeries builds a Series and computes its statistics from Values only.
func NewSeries(name, unit, note string, warmup, values []float64) Series {
	return Series{Name: name, Unit: unit, Note: note, Warmup: warmup, Values: values, Stats: Summarize(values)}
}

// Result is the JSON document written for one scenario run.
type Result struct {
	SchemaVersion int            `json:"schema_version"`
	Scenario      string         `json:"scenario"`
	Source        string         `json:"source"`
	Command       string         `json:"command"`
	Params        map[string]any `json:"params"`
	Env           Environment    `json:"environment"`
	Series        []Series       `json:"series"`
	Notes         []string       `json:"notes,omitempty"`
	Invalid       bool           `json:"invalid"`
}

// Finish stamps the end time, validates the environment and marks the result.
func (r *Result) Finish(end time.Time) {
	r.Env.EndedAt = end.UTC().Format(time.RFC3339)
	r.Env.Missing = r.Env.Validate()
	r.Invalid = len(r.Env.Missing) > 0
}

// JSON renders the result, or refuses when the environment record is incomplete.
func (r *Result) JSON() ([]byte, error) {
	if r.Invalid {
		return nil, fmt.Errorf("invalid result: environment record incomplete (%s)", strings.Join(r.Env.Missing, ", "))
	}
	return json.MarshalIndent(r, "", "  ")
}

// Markdown renders the short summary.
func (r *Result) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# mcbench %s\n\n", r.Scenario)
	b.WriteString("> This is a baseline of THIS machine and these settings. It is not a performance claim.\n\n")
	fmt.Fprintf(&b, "Source: %s. Command: `%s`\n\n", r.Source, r.Command)
	e := r.Env
	fmt.Fprintf(&b, "Environment: commit %s (dirty=%v), %s, %s, %s (%d cores), RAM %d kB, PostgreSQL %s, policy %s (%s), slots %d, workers %d, N %d, load before %.2f/%.2f/%.2f, after %.2f/%.2f/%.2f, %s to %s, idle check %s (max-load %.2f).\n\n",
		e.Commit, e.Dirty, e.GoVersion, e.OS+" "+e.Kernel, e.CPUModel, e.CPUCores, e.RAMTotalKB, e.PostgresVer, e.Policy, e.PolicySource,
		e.Slots, e.Workers, e.N, e.LoadBefore.One, e.LoadBefore.Five, e.LoadBefore.Fifteen,
		e.LoadAfter.One, e.LoadAfter.Five, e.LoadAfter.Fifteen, e.StartedAt, e.EndedAt, e.IdleCheck, e.MaxLoad)
	b.WriteString("| series | unit | N | min | median | p95 | max | warm-up discarded |\n|---|---|---|---|---|---|---|---|\n")
	for _, s := range r.Series {
		if s.Stats.N == 0 {
			fmt.Fprintf(&b, "| %s | %s | 0 | n/a | n/a | n/a | n/a | %d |\n", s.Name, s.Unit, len(s.Warmup))
			continue
		}
		p95 := fmt.Sprintf("%.3f", s.Stats.P95)
		if s.Stats.P95IsMax {
			p95 += " (= max, N<20)"
		}
		fmt.Fprintf(&b, "| %s | %s | %d | %.3f | %.3f | %s | %.3f | %d |\n", s.Name, s.Unit, s.Stats.N, s.Stats.Min, s.Stats.Median, p95, s.Stats.Max, len(s.Warmup))
	}
	for _, s := range r.Series {
		if s.Note != "" {
			fmt.Fprintf(&b, "\n- %s: %s", s.Name, s.Note)
		}
	}
	for _, n := range r.Notes {
		fmt.Fprintf(&b, "\n- %s", n)
	}
	b.WriteString("\n")
	return b.String()
}
