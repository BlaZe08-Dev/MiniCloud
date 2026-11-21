package storagetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
)

// Shared ADR-0022 node conformance cases (slice V2-1b). The in-memory and the PostgreSQL
// repositories run the same cases through a NodeHarness. Time is the repository clock: the injected
// clock in memory, the database clock in PostgreSQL, which cannot be moved, so the PostgreSQL
// harness moves every stored node timestamp back instead (never a sleep).
//
// The rate limit decision itself is one pure function, domain.HardwareUpdateLimit, whose boundary
// (elapsed == interval minus, equal to and plus one nanosecond) is proven by
// TestDomain_HardwareUpdateLimit. PostgreSQL's clock cannot be made to land on the boundary, so these
// cases prove that both repositories call that function by testing with margins on both sides: an
// interval minus one second is rejected and an interval plus one second is accepted.

// NodeTestMinInterval is the minimum hardware interval these cases pass to RegisterNode.
const NodeTestMinInterval = 60 * time.Second

// NodeHardwareRow is one stored node_hardware row, every column. Timestamps are UTC RFC 3339 strings
// so rows compare with reflect.DeepEqual.
type NodeHardwareRow struct {
	Version         int
	ContractVersion int
	AgentVersion    string
	Hardware        domain.NodeHardware
	ContentHash     string
	ReportedAt      string
}

// NodeTelemetryRow is the stored node_telemetry row, every column.
type NodeTelemetryRow struct {
	ContractVersion int
	SessionID       string
	Seq             int64
	ReceivedAt      string
	SampledAt       string
	Telemetry       domain.NodeTelemetry
}

// NodeDump is the raw stored state of one node: the nodes row, every hardware row in ascending
// version, and the telemetry row (nil when none). It is read straight from storage, so it sees
// columns the NodeView hides (session_id, seq, content_hash).
type NodeDump struct {
	Exists      bool
	DisplayName string
	CreatedAt   string
	LastSeenAt  string
	Hardware    []NodeHardwareRow
	Telemetry   *NodeTelemetryRow
}

// NodeHarness pairs a repository with the backend-specific hooks the node cases need.
type NodeHarness struct {
	Repo storage.Repository
	// Advance moves repository time forward by d (the injected clock in memory; in PostgreSQL every
	// stored node timestamp is moved back by d, which is the same for every comparison made).
	Advance func(t *testing.T, d time.Duration)
	// Dump reads the raw stored state of a node.
	Dump func(t *testing.T, nodeID string) NodeDump
}

func i64p(v int64) *int64         { return &v }
func strp(v string) *string       { return &v }
func sameDump(a, b NodeDump) bool { return reflect.DeepEqual(a, b) }

// NodeTestID returns the n-th deterministic, valid node (or session) UUID.
func NodeTestID(n int) string {
	return fmt.Sprintf("%08x-0000-4000-8000-%012x", 0xabc00000+n, n)
}

// nodeHW returns a complete hardware inventory that differs for every value of mem.
func nodeHW(mem int64) domain.NodeHardware {
	return domain.NodeHardware{
		OS: "linux", Arch: "amd64", CPUModel: strp("Test CPU"), CPULogicalCores: i64p(8), CPUPhysicalCores: i64p(4),
		MemoryTotalBytes: i64p(mem), DiskTotalBytes: i64p(1 << 40),
	}
}

func nodeRegistration(id, name string, mem int64) domain.NodeRegistration {
	return domain.NodeRegistration{NodeID: id, DisplayName: name, ContractVersion: domain.ContractVersion, AgentVersion: "v1.0.0", Hardware: nodeHW(mem)}
}

func telemetryReport(session string, seq, ageMS, load int64) domain.NodeTelemetryReport {
	return domain.NodeTelemetryReport{
		ContractVersion: domain.ContractVersion, SessionID: session, Seq: seq, SampleAgeMS: ageMS,
		NodeTelemetry: domain.NodeTelemetry{CPULoad1mMilli: i64p(load), MemoryAvailableBytes: i64p(1000 + load), DiskFreeBytes: nil},
	}
}

func mustRegister(t *testing.T, h NodeHarness, reg domain.NodeRegistration) *domain.NodeRegistrationResult {
	t.Helper()
	res, err := h.Repo.RegisterNode(context.Background(), reg, NodeTestMinInterval)
	if err != nil {
		t.Fatalf("RegisterNode(%s, %q, mem=%d): %v", reg.NodeID, reg.DisplayName, *reg.Hardware.MemoryTotalBytes, err)
	}
	return res
}

func mustTelemetry(t *testing.T, h NodeHarness, nodeID string, r domain.NodeTelemetryReport) {
	t.Helper()
	if err := h.Repo.RecordNodeTelemetry(context.Background(), nodeID, r); err != nil {
		t.Fatalf("RecordNodeTelemetry(session %s seq %d): %v", r.SessionID, r.Seq, err)
	}
}

func mustGetNode(t *testing.T, h NodeHarness, id string) *domain.NodeView {
	t.Helper()
	v, err := h.Repo.GetNode(context.Background(), id)
	if err != nil {
		t.Fatalf("GetNode(%s): %v", id, err)
	}
	return v
}

func hwVersions(d NodeDump) []int {
	out := make([]int, 0, len(d.Hardware))
	for _, r := range d.Hardware {
		out = append(out, r.Version)
	}
	return out
}

func expectRateLimited(t *testing.T, err error, wantRetry time.Duration) {
	t.Helper()
	if !errors.Is(err, domain.ErrNodeRateLimited) {
		t.Fatalf("expected a rate limit error, got %v", err)
	}
	var rl *domain.NodeRateLimitedError
	if !errors.As(err, &rl) {
		t.Fatalf("rate limit error %T does not carry RetryAfter", err)
	}
	if rl.RetryAfter != wantRetry || rl.RetryAfter%time.Second != 0 {
		t.Fatalf("RetryAfter = %v, want exactly %v (whole seconds)", rl.RetryAfter, wantRetry)
	}
}

// RunNodeRegisterIdempotentConformance: the same ID registering twice leaves one nodes row and
// updates display_name; a second ID is a second row. (ADR-0022 Section 8.1, mutant N1.)
func RunNodeRegisterIdempotentConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	ctx := context.Background()
	a, b := NodeTestID(1), NodeTestID(2)

	if res := mustRegister(t, h, nodeRegistration(a, "first", 100)); res.HardwareVersion != 1 {
		t.Fatalf("first registration hardware version = %d, want 1", res.HardwareVersion)
	}
	h.Advance(t, time.Second)
	if res := mustRegister(t, h, nodeRegistration(a, "renamed", 100)); res.HardwareVersion != 1 {
		t.Fatalf("second registration hardware version = %d, want 1", res.HardwareVersion)
	}
	nodes, err := h.Repo.ListNodes(ctx)
	if err != nil || len(nodes) != 1 {
		t.Fatalf("after two registrations of one ID: ListNodes = %d nodes (err=%v), want 1", len(nodes), err)
	}
	v := mustGetNode(t, h, a)
	if v.DisplayName != "renamed" {
		t.Fatalf("display_name = %q, want \"renamed\"", v.DisplayName)
	}
	d := h.Dump(t, a)
	// The row is the same row, not a new one: its created_at is still the first registration's, one
	// second (plus the real time the test took, in PostgreSQL) before the second registration wrote
	// last_seen_at. A new row would have created_at == last_seen_at.
	created, err1 := time.Parse(time.RFC3339Nano, d.CreatedAt)
	seen, err2 := time.Parse(time.RFC3339Nano, d.LastSeenAt)
	if err1 != nil || err2 != nil {
		t.Fatalf("unparseable node timestamps: %q %q", d.CreatedAt, d.LastSeenAt)
	}
	if gap := seen.Sub(created); gap < time.Second || gap > time.Second+2*time.Second {
		t.Fatalf("last_seen_at - created_at = %v, want about 1s: the node row was replaced instead of updated", gap)
	}
	if len(d.Hardware) != 1 {
		t.Fatalf("hardware rows = %v, want exactly version 1", hwVersions(d))
	}

	mustRegister(t, h, nodeRegistration(b, "other", 100))
	if nodes, _ := h.Repo.ListNodes(ctx); len(nodes) != 2 {
		t.Fatalf("a second node ID did not create a second node: %d nodes", len(nodes))
	}
}

// RunNodeInvalidRegistrationConformance: an invalid registration is rejected with ErrInvalidRequest
// and writes nothing.
func RunNodeInvalidRegistrationConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	ctx := context.Background()
	bad := map[string]domain.NodeRegistration{}
	r := nodeRegistration(NodeTestID(1), "n", 1)
	r.NodeID = "NOT-A-UUID"
	bad["bad node id"] = r
	r = nodeRegistration(NodeTestID(1), "n", 1)
	r.ContractVersion = 99
	bad["unsupported contract version"] = r
	r = nodeRegistration(NodeTestID(1), "tab\there", 1)
	bad["control character in display_name"] = r
	r = nodeRegistration(NodeTestID(1), "n", -1)
	bad["negative memory"] = r
	for name, reg := range bad {
		_, err := h.Repo.RegisterNode(ctx, reg, NodeTestMinInterval)
		if !errors.Is(err, domain.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
	if nodes, err := h.Repo.ListNodes(ctx); err != nil || len(nodes) != 0 {
		t.Fatalf("rejected registrations left %d nodes (err=%v)", len(nodes), err)
	}
}

// RunNodeHardwareVersionOnChangeOnlyConformance: the same content never adds a version (even long
// after); changed content adds exactly version + 1; a new agent_version alone is not a change.
// (Mutant N2.)
func RunNodeHardwareVersionOnChangeOnlyConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	if res := mustRegister(t, h, nodeRegistration(id, "n", 100)); res.HardwareVersion != 1 {
		t.Fatalf("version = %d, want 1", res.HardwareVersion)
	}
	h.Advance(t, NodeTestMinInterval+time.Second)
	if res := mustRegister(t, h, nodeRegistration(id, "n", 100)); res.HardwareVersion != 1 {
		t.Fatalf("same content after the interval: version = %d, want 1 (no new version)", res.HardwareVersion)
	}
	if got := hwVersions(h.Dump(t, id)); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("hardware rows after an unchanged registration = %v, want [1]", got)
	}

	if res := mustRegister(t, h, nodeRegistration(id, "n", 200)); res.HardwareVersion != 2 {
		t.Fatalf("changed content: version = %d, want 2", res.HardwareVersion)
	}
	h.Advance(t, NodeTestMinInterval+time.Second)
	agent := nodeRegistration(id, "n", 200)
	agent.AgentVersion = "v9.9.9"
	if res := mustRegister(t, h, agent); res.HardwareVersion != 2 {
		t.Fatalf("only agent_version changed: version = %d, want 2 (agent_version is not hashed)", res.HardwareVersion)
	}
	h.Advance(t, NodeTestMinInterval+time.Second)
	if res := mustRegister(t, h, nodeRegistration(id, "n", 300)); res.HardwareVersion != 3 {
		t.Fatalf("second change: version = %d, want 3", res.HardwareVersion)
	}
	d := h.Dump(t, id)
	if got := hwVersions(d); !reflect.DeepEqual(got, []int{1, 2, 3}) {
		t.Fatalf("hardware rows = %v, want [1 2 3]", got)
	}
	for _, row := range d.Hardware {
		want := nodeHW(int64(row.Version) * 100).ContentHash()
		if row.ContentHash != want {
			t.Errorf("version %d stored content_hash = %s, want the domain ContentHash %s", row.Version, row.ContentHash, want)
		}
	}
	if cur := mustGetNode(t, h, id).Hardware; cur == nil || cur.Version != 3 || *cur.MemoryTotalBytes != 300 {
		t.Fatalf("current hardware view = %+v, want version 3 with memory 300", cur)
	}
}

// RunNodeHardwareHistoryKeepsLatest10Conformance: after 25 accepted changes exactly the newest 10
// versions remain, the current one is the highest, and an 11th row never exists. (Mutant N10.)
func RunNodeHardwareHistoryKeepsLatest10Conformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	const changes = 25
	for i := 1; i <= changes; i++ {
		if i > 1 {
			h.Advance(t, NodeTestMinInterval+time.Second)
		}
		if res := mustRegister(t, h, nodeRegistration(id, "n", int64(i))); res.HardwareVersion != i {
			t.Fatalf("change %d: version = %d, want %d", i, res.HardwareVersion, i)
		}
		d := h.Dump(t, id)
		if len(d.Hardware) > domain.MaxHardwareHistory {
			t.Fatalf("after change %d the node holds %d hardware rows, want at most %d: %v", i, len(d.Hardware), domain.MaxHardwareHistory, hwVersions(d))
		}
		if last := d.Hardware[len(d.Hardware)-1]; last.Version != i {
			t.Fatalf("after change %d the current (highest) row is version %d", i, last.Version)
		}
	}
	d := h.Dump(t, id)
	want := make([]int, 0, 10)
	for v := changes - domain.MaxHardwareHistory + 1; v <= changes; v++ {
		want = append(want, v)
	}
	if got := hwVersions(d); !reflect.DeepEqual(got, want) {
		t.Fatalf("surviving versions = %v, want exactly the newest 10: %v", got, want)
	}
	if cur := mustGetNode(t, h, id).Hardware; cur == nil || cur.Version != changes || *cur.MemoryTotalBytes != changes {
		t.Fatalf("current hardware = %+v, want version %d", cur, changes)
	}
}

// RunNodeHardwareRateLimitRejectsInsideIntervalConformance: a changed hardware content inside the
// interval is rejected with the rate limit error and writes nothing: display_name, last_seen_at and
// the hardware history stay byte-identical. (Mutants N13, N15.)
func RunNodeHardwareRateLimitRejectsInsideIntervalConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "kept", 100))
	h.Advance(t, 10*time.Second)
	before := h.Dump(t, id)

	_, err := h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "must-not-be-written", 200), NodeTestMinInterval)
	expectRateLimited(t, err, 50*time.Second)
	after := h.Dump(t, id)
	if after.DisplayName != "kept" || after.LastSeenAt != before.LastSeenAt {
		t.Fatalf("a rejected registration changed the nodes row: before %+v after %+v", before, after)
	}
	if !sameDump(before, after) {
		t.Fatalf("a rejected registration changed stored state:\nbefore %+v\nafter  %+v", before, after)
	}

	// Margin: one second inside the interval is still rejected, with a retry hint of one second.
	h.Advance(t, NodeTestMinInterval-10*time.Second-2*time.Second)
	before = h.Dump(t, id)
	_, err = h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "must-not-be-written", 200), NodeTestMinInterval)
	expectRateLimited(t, err, 2*time.Second)
	if after := h.Dump(t, id); !sameDump(before, after) {
		t.Fatalf("a rejected registration (interval minus 2 s) changed stored state")
	}
}

// RunNodeHardwareRateLimitAllowsAfterIntervalConformance: the same change is accepted once the
// interval has passed (repository time moved, not a sleep). Margins on both sides of the interval.
func RunNodeHardwareRateLimitAllowsAfterIntervalConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))

	h.Advance(t, NodeTestMinInterval-time.Second)
	if _, err := h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "n", 200), NodeTestMinInterval); !errors.Is(err, domain.ErrNodeRateLimited) {
		t.Fatalf("interval minus 1 s: err = %v, want a rate limit rejection", err)
	}
	h.Advance(t, 2*time.Second) // now interval plus 1 s after the stored reported_at
	res, err := h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "n", 200), NodeTestMinInterval)
	if err != nil {
		t.Fatalf("interval plus 1 s: %v", err)
	}
	if res.HardwareVersion != 2 {
		t.Fatalf("version = %d, want 2", res.HardwareVersion)
	}
	// The accepted write restarts the interval.
	if _, err := h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "n", 300), NodeTestMinInterval); !errors.Is(err, domain.ErrNodeRateLimited) {
		t.Fatalf("a further change right after an accepted one: err = %v, want a rate limit rejection", err)
	}
}

// RunNodeHardwareRateLimitUnchangedNeverLimitedConformance: an unchanged hash is never limited and
// never adds a hardware row; the nodes row is written only when the name changed or last_seen_at is
// older than the interval. (Mutant N14.)
func RunNodeHardwareRateLimitUnchangedNeverLimitedConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	first := h.Dump(t, id)

	for i := 0; i < 5; i++ {
		if res := mustRegister(t, h, nodeRegistration(id, "n", 100)); res.HardwareVersion != 1 {
			t.Fatalf("repeat %d: version = %d, want 1", i, res.HardwareVersion)
		}
	}
	if d := h.Dump(t, id); !sameDump(first, d) {
		t.Fatalf("unchanged registrations inside the interval wrote something:\nbefore %+v\nafter  %+v", first, d)
	}

	// Still inside the interval, but the name changed: the nodes row is written (and only it).
	h.Advance(t, 5*time.Second)
	first = h.Dump(t, id) // re-read after the advance (PostgreSQL moves stored timestamps)
	mustRegister(t, h, nodeRegistration(id, "renamed", 100))
	d := h.Dump(t, id)
	if d.DisplayName != "renamed" || d.LastSeenAt == first.LastSeenAt {
		t.Fatalf("a name change was not written: %+v", d)
	}
	if !reflect.DeepEqual(d.Hardware, first.Hardware) {
		t.Fatalf("a name change touched the hardware rows")
	}
	afterRename := d

	// Same name, interval minus 2 s since last_seen_at: not written.
	h.Advance(t, NodeTestMinInterval-2*time.Second)
	afterRename = h.Dump(t, id)
	mustRegister(t, h, nodeRegistration(id, "renamed", 100))
	if d := h.Dump(t, id); !sameDump(afterRename, d) {
		t.Fatalf("an unchanged registration inside the interval wrote the nodes row")
	}
	// Same name, interval plus 1 s since last_seen_at: last_seen_at is refreshed.
	h.Advance(t, 3*time.Second)
	afterRename = h.Dump(t, id)
	hwBefore := afterRename.Hardware
	mustRegister(t, h, nodeRegistration(id, "renamed", 100))
	d = h.Dump(t, id)
	if d.LastSeenAt == afterRename.LastSeenAt {
		t.Fatalf("last_seen_at older than the interval was not refreshed")
	}
	if !reflect.DeepEqual(d.Hardware, hwBefore) {
		t.Fatalf("a last_seen_at refresh touched the hardware rows")
	}
}

// RunNodeRateLimitErrorCarriesRetryAfterConformance: the rate limit error matches
// domain.ErrNodeRateLimited and carries RetryAfter as exact whole seconds, rounded up from the
// repository clock.
func RunNodeRateLimitErrorCarriesRetryAfterConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	steps := []struct {
		advance time.Duration // cumulative elapsed after this step: 0, 10 s, 30.25 s, 59.5 s
		want    time.Duration
	}{
		{0, 60 * time.Second},                                     // 60 s minus a few ms rounds up to 60
		{10 * time.Second, 50 * time.Second},                      // 49.99 s rounds up to 50
		{20*time.Second + 250*time.Millisecond, 30 * time.Second}, // 29.75 s rounds up to 30
		{29*time.Second + 250*time.Millisecond, 1 * time.Second},  // 0.5 s rounds up to 1
	}
	for _, s := range steps {
		if s.advance > 0 {
			h.Advance(t, s.advance)
		}
		_, err := h.Repo.RegisterNode(context.Background(), nodeRegistration(id, "n", 200), NodeTestMinInterval)
		expectRateLimited(t, err, s.want)
		var rl *domain.NodeRateLimitedError
		if errors.As(err, &rl) && time.Duration(rl.RetryAfterSeconds())*time.Second != s.want {
			t.Fatalf("RetryAfterSeconds = %d, want %d", rl.RetryAfterSeconds(), int(s.want/time.Second))
		}
	}
}

// RunNodeTelemetryOutOfOrderRejectedConformance: same session, a lower seq is rejected with
// ErrTelemetryOutOfOrder and the stored row (every column, received_at included) is unchanged.
// (Mutants N20, N23.)
func RunNodeTelemetryOutOfOrderRejectedConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	s := NodeTestID(100)
	mustTelemetry(t, h, id, telemetryReport(s, 5, 120, 500))
	h.Advance(t, time.Second) // a rewritten received_at would now differ
	before := h.Dump(t, id)

	for _, seq := range []int64{4, 3, 1} {
		err := h.Repo.RecordNodeTelemetry(context.Background(), id, telemetryReport(s, seq, 999, 9000+seq))
		if !errors.Is(err, domain.ErrTelemetryOutOfOrder) {
			t.Fatalf("seq %d after 5 in the same session: err = %v, want ErrTelemetryOutOfOrder", seq, err)
		}
		if after := h.Dump(t, id); !sameDump(before, after) {
			t.Fatalf("a rejected report (seq %d) changed stored state:\nbefore %+v\nafter  %+v", seq, before, after)
		}
	}
	if before.Telemetry == nil || before.Telemetry.Seq != 5 {
		t.Fatalf("stored telemetry = %+v, want seq 5", before.Telemetry)
	}
	// A greater seq of the same session is accepted and replaces the sample.
	mustTelemetry(t, h, id, telemetryReport(s, 6, 0, 700))
	if tv := mustGetNode(t, h, id).Telemetry; tv == nil || *tv.CPULoad1mMilli != 700 {
		t.Fatalf("telemetry after seq 6 = %+v, want load 700", tv)
	}
}

// RunNodeTelemetryReplaySameSeqRejectedConformance: same session, equal seq is rejected and the
// stored row is unchanged. (Mutant N21.)
func RunNodeTelemetryReplaySameSeqRejectedConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	s := NodeTestID(100)
	mustTelemetry(t, h, id, telemetryReport(s, 7, 10, 500))
	h.Advance(t, time.Second)
	before := h.Dump(t, id)
	err := h.Repo.RecordNodeTelemetry(context.Background(), id, telemetryReport(s, 7, 20, 600))
	if !errors.Is(err, domain.ErrTelemetryOutOfOrder) {
		t.Fatalf("replay of seq 7: err = %v, want ErrTelemetryOutOfOrder", err)
	}
	if after := h.Dump(t, id); !sameDump(before, after) {
		t.Fatalf("a replayed report changed stored state:\nbefore %+v\nafter  %+v", before, after)
	}
}

// RunNodeTelemetryNewSessionAcceptedAfterRestartConformance: a new session_id with seq 1 is accepted
// when the stored seq is far higher and replaces the sample; the same seq in a different session is
// accepted. (Mutant N22.)
func RunNodeTelemetryNewSessionAcceptedAfterRestartConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	s1, s2, s3 := NodeTestID(101), NodeTestID(102), NodeTestID(103)

	mustTelemetry(t, h, id, telemetryReport(s1, 100000, 10, 500))
	h.Advance(t, time.Second)
	mustTelemetry(t, h, id, telemetryReport(s2, 1, 10, 800)) // worker restart: new session, seq 1
	d := h.Dump(t, id)
	if d.Telemetry == nil || d.Telemetry.SessionID != s2 || d.Telemetry.Seq != 1 || *d.Telemetry.Telemetry.CPULoad1mMilli != 800 {
		t.Fatalf("stored telemetry after the restart = %+v, want session %s seq 1 load 800", d.Telemetry, s2)
	}
	// The new session continues strictly: seq 1 again is a replay.
	if err := h.Repo.RecordNodeTelemetry(context.Background(), id, telemetryReport(s2, 1, 10, 900)); !errors.Is(err, domain.ErrTelemetryOutOfOrder) {
		t.Fatalf("seq 1 of the live session again: err = %v, want ErrTelemetryOutOfOrder", err)
	}
	// Same seq in a different session is accepted.
	mustTelemetry(t, h, id, telemetryReport(s3, 1, 10, 950))
	if d := h.Dump(t, id); d.Telemetry.SessionID != s3 || *d.Telemetry.Telemetry.CPULoad1mMilli != 950 {
		t.Fatalf("same seq in a different session was not accepted: %+v", d.Telemetry)
	}
}

// RunNodeTelemetryOlderSessionAfterNewerIsAcceptedConformance documents a KNOWN WEAKNESS of the
// ADR-0022 Section 3.3 rule (task V2-1b clarification K4; threat T11): a report from an OLDER
// session that arrives after a newer session's report has a different session_id and is therefore
// ACCEPTED and replaces the stored sample; the next report of the live session is accepted again for
// the same reason. This test pins the behaviour as the rule defines it. It is not a requirement that
// a late old-session report be accepted, and no new transition rule is invented: the ADR amendment
// owns any change.
func RunNodeTelemetryOlderSessionAfterNewerIsAcceptedConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	older, newer := NodeTestID(101), NodeTestID(102)

	mustTelemetry(t, h, id, telemetryReport(older, 5, 10, 500))
	mustTelemetry(t, h, id, telemetryReport(newer, 3, 10, 600)) // the worker restarted
	// A delayed report of the OLDER session arrives now: accepted, replaces the newer sample.
	mustTelemetry(t, h, id, telemetryReport(older, 6, 10, 700))
	if d := h.Dump(t, id); d.Telemetry.SessionID != older || d.Telemetry.Seq != 6 || *d.Telemetry.Telemetry.CPULoad1mMilli != 700 {
		t.Fatalf("older session after newer: stored %+v, want the older session's seq 6 (accepted by the rule)", d.Telemetry)
	}
	// The live (newer) session's next report is accepted again, because its session differs now.
	mustTelemetry(t, h, id, telemetryReport(newer, 4, 10, 800))
	if d := h.Dump(t, id); d.Telemetry.SessionID != newer || d.Telemetry.Seq != 4 {
		t.Fatalf("live session next report: stored %+v, want the newer session's seq 4", d.Telemetry)
	}
}

// RunNodeTelemetryUnknownNodeConformance: telemetry for an unregistered node is ErrNodeNotFound,
// for a first report and for a high seq, never ErrTelemetryOutOfOrder, and nothing is created.
// (Mutant N31.)
func RunNodeTelemetryUnknownNodeConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	unknown := NodeTestID(9)
	for _, seq := range []int64{1, 2, 1 << 40, domain.MaxTelemetrySeq} {
		err := h.Repo.RecordNodeTelemetry(context.Background(), unknown, telemetryReport(NodeTestID(100), seq, 0, 1))
		if !errors.Is(err, domain.ErrNodeNotFound) || errors.Is(err, domain.ErrTelemetryOutOfOrder) {
			t.Fatalf("seq %d for an unknown node: err = %v, want ErrNodeNotFound", seq, err)
		}
	}
	if _, err := h.Repo.GetNode(context.Background(), unknown); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("GetNode of an unknown node: err = %v, want ErrNodeNotFound", err)
	}
	if d := h.Dump(t, unknown); d.Exists || d.Telemetry != nil {
		t.Fatalf("telemetry for an unknown node created state: %+v", d)
	}
	// An invalid report is a request error whatever the node.
	bad := telemetryReport("nope", 1, 0, 1)
	if err := h.Repo.RecordNodeTelemetry(context.Background(), unknown, bad); !errors.Is(err, domain.ErrInvalidRequest) {
		t.Fatalf("invalid report: err = %v, want ErrInvalidRequest", err)
	}
}

// RunNodeTelemetryDoesNotTouchHardwareConformance: telemetry leaves the node_hardware rows and the
// nodes row (display_name, created_at, last_seen_at) byte-identical. (Mutant N3.)
func RunNodeTelemetryDoesNotTouchHardwareConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	id := NodeTestID(1)
	mustRegister(t, h, nodeRegistration(id, "n", 100))
	h.Advance(t, 5*time.Second)
	before := h.Dump(t, id)
	s := NodeTestID(100)
	// No Advance between the reports: in PostgreSQL an advance would move the stored timestamps and
	// the comparison below must see only what telemetry itself writes.
	for seq := int64(1); seq <= 3; seq++ {
		mustTelemetry(t, h, id, telemetryReport(s, seq, 50, 100*seq))
	}
	after := h.Dump(t, id)
	if after.Telemetry == nil {
		t.Fatal("no telemetry stored")
	}
	if !reflect.DeepEqual(before.Hardware, after.Hardware) {
		t.Fatalf("telemetry changed the hardware rows:\nbefore %+v\nafter  %+v", before.Hardware, after.Hardware)
	}
	if before.DisplayName != after.DisplayName || before.CreatedAt != after.CreatedAt || before.LastSeenAt != after.LastSeenAt {
		t.Fatalf("telemetry changed the nodes row:\nbefore %+v\nafter  %+v", before, after)
	}
}

// RunNodeUnknownStaysNullConformance: absent fields read back nil, not 0; a sent 0 reads back 0.
// (Mutant N4.)
func RunNodeUnknownStaysNullConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	unknownID, zeroID := NodeTestID(1), NodeTestID(2)

	unknown := domain.NodeRegistration{NodeID: unknownID, ContractVersion: 1, Hardware: domain.NodeHardware{OS: "linux", Arch: "amd64"}}
	mustRegister(t, h, unknown)
	hw := mustGetNode(t, h, unknownID).Hardware
	if hw == nil {
		t.Fatal("no hardware view")
	}
	if hw.CPUModel != nil || hw.CPULogicalCores != nil || hw.CPUPhysicalCores != nil || hw.MemoryTotalBytes != nil ||
		hw.DiskTotalBytes != nil || hw.GPUCount != nil || hw.GPUModel != nil || hw.GPUVRAMTotalBytes != nil {
		t.Fatalf("absent hardware fields did not read back as nil: %+v", hw.NodeHardware)
	}
	mustTelemetry(t, h, unknownID, domain.NodeTelemetryReport{ContractVersion: 1, SessionID: NodeTestID(100), Seq: 1})
	if tv := mustGetNode(t, h, unknownID).Telemetry; tv == nil || tv.CPULoad1mMilli != nil || tv.MemoryAvailableBytes != nil || tv.DiskFreeBytes != nil {
		t.Fatalf("absent telemetry fields did not read back as nil: %+v", tv)
	}

	zero := domain.NodeRegistration{NodeID: zeroID, ContractVersion: 1, Hardware: domain.NodeHardware{
		OS: "linux", Arch: "amd64", MemoryTotalBytes: i64p(0), DiskTotalBytes: i64p(0), GPUCount: i64p(0), GPUVRAMTotalBytes: i64p(0)}}
	mustRegister(t, h, zero)
	hw = mustGetNode(t, h, zeroID).Hardware
	for name, p := range map[string]*int64{"memory_total_bytes": hw.MemoryTotalBytes, "disk_total_bytes": hw.DiskTotalBytes, "gpu_count": hw.GPUCount, "gpu_vram_total_bytes": hw.GPUVRAMTotalBytes} {
		if p == nil || *p != 0 {
			t.Errorf("a sent 0 for %s read back as %v, want 0", name, p)
		}
	}
	if hw.CPUModel != nil || hw.CPULogicalCores != nil {
		t.Errorf("fields that were not sent read back non-nil next to sent zeros: %+v", hw.NodeHardware)
	}
	z := int64(0)
	mustTelemetry(t, h, zeroID, domain.NodeTelemetryReport{ContractVersion: 1, SessionID: NodeTestID(101), Seq: 1,
		NodeTelemetry: domain.NodeTelemetry{CPULoad1mMilli: &z, MemoryAvailableBytes: &z, DiskFreeBytes: &z}})
	tv := mustGetNode(t, h, zeroID).Telemetry
	if tv == nil || tv.CPULoad1mMilli == nil || *tv.CPULoad1mMilli != 0 || tv.MemoryAvailableBytes == nil || *tv.MemoryAvailableBytes != 0 || tv.DiskFreeBytes == nil || *tv.DiskFreeBytes != 0 {
		t.Fatalf("sent 0 telemetry did not read back as 0: %+v", tv)
	}
	// Distinct unknown versus zero content hashes are stored distinctly.
	if hu, hz := h.Dump(t, unknownID).Hardware[0].ContentHash, h.Dump(t, zeroID).Hardware[0].ContentHash; hu == hz {
		t.Fatal("unknown and zero hardware stored the same content_hash")
	}
}

// RunNodeViewsConformance: the read model. ListNodes is ordered by ID; WorkerIDs are the workers
// that name the node, sorted; telemetry shows received_at and sampled_at = received_at - the sample
// age and nothing else of the write path; a node without telemetry reads nil.
func RunNodeViewsConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	ctx := context.Background()
	b, a := NodeTestID(2), NodeTestID(1)
	mustRegister(t, h, nodeRegistration(b, "bee", 100))
	mustRegister(t, h, nodeRegistration(a, "ay", 100))
	for _, w := range []struct{ id, node string }{{"w_z", a}, {"w_a", a}, {"w_m", b}} {
		node := w.node
		if err := h.Repo.UpsertWorker(ctx, &domain.Worker{ID: w.id, Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &node}); err != nil {
			t.Fatalf("UpsertWorker(%s): %v", w.id, err)
		}
	}
	if err := h.Repo.UpsertWorker(ctx, &domain.Worker{ID: "w_legacy", Version: "v", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}}); err != nil {
		t.Fatal(err)
	}
	mustTelemetry(t, h, a, telemetryReport(NodeTestID(100), 1, 1500, 321))

	nodes, err := h.Repo.ListNodes(ctx)
	if err != nil || len(nodes) != 2 || nodes[0].ID != a || nodes[1].ID != b {
		t.Fatalf("ListNodes = %v (err=%v), want [%s %s]", nodes, err, a, b)
	}
	if !reflect.DeepEqual(nodes[0].WorkerIDs, []string{"w_a", "w_z"}) || !reflect.DeepEqual(nodes[1].WorkerIDs, []string{"w_m"}) {
		t.Fatalf("worker IDs = %v and %v, want [w_a w_z] and [w_m]", nodes[0].WorkerIDs, nodes[1].WorkerIDs)
	}
	if nodes[1].Telemetry != nil {
		t.Fatalf("node without telemetry has a telemetry view: %+v", nodes[1].Telemetry)
	}
	tv := nodes[0].Telemetry
	if tv == nil || *tv.CPULoad1mMilli != 321 || tv.ContractVersion != 1 {
		t.Fatalf("telemetry view = %+v", tv)
	}
	if got := tv.ReceivedAt.Sub(tv.SampledAt); got != 1500*time.Millisecond {
		t.Fatalf("received_at - sampled_at = %v, want 1.5s (the sample age)", got)
	}
	one, err := h.Repo.GetNode(ctx, a)
	if err != nil || !reflect.DeepEqual(one, nodes[0]) {
		t.Fatalf("GetNode and ListNodes disagree: %+v vs %+v (err=%v)", one, nodes[0], err)
	}
	if one.Hardware == nil || one.Hardware.Version != 1 || one.Hardware.AgentVersion != "v1.0.0" || one.Hardware.ContractVersion != 1 || one.Hardware.ReportedAt.IsZero() {
		t.Fatalf("hardware view = %+v", one.Hardware)
	}
}

// RunWorkerNodeIDRoundTripConformance: node_id is stored, read back by GetWorker and ListWorkers,
// shown on the node view, and reset to nil by a registration without it. (Mutant N8.)
func RunWorkerNodeIDRoundTripConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	ctx := context.Background()
	n1, n2 := NodeTestID(1), NodeTestID(2)
	mustRegister(t, h, nodeRegistration(n1, "one", 100))
	mustRegister(t, h, nodeRegistration(n2, "two", 100))
	upsert := func(node *string) {
		t.Helper()
		w := &domain.Worker{ID: "w1", Version: "v1", TotalSlots: 2, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: node}
		if err := h.Repo.UpsertWorker(ctx, w); err != nil {
			t.Fatalf("UpsertWorker: %v", err)
		}
	}
	nodeOf := func() *string {
		t.Helper()
		w, err := h.Repo.GetWorker(ctx, "w1")
		if err != nil {
			t.Fatalf("GetWorker: %v", err)
		}
		ws, err := h.Repo.ListWorkers(ctx)
		if err != nil || len(ws) != 1 {
			t.Fatalf("ListWorkers: %v (err=%v)", ws, err)
		}
		if (w.NodeID == nil) != (ws[0].NodeID == nil) || (w.NodeID != nil && *w.NodeID != *ws[0].NodeID) {
			t.Fatalf("GetWorker and ListWorkers disagree on node_id: %v vs %v", w.NodeID, ws[0].NodeID)
		}
		return w.NodeID
	}

	upsert(nil)
	if got := nodeOf(); got != nil {
		t.Fatalf("a worker registered without node_id reads %q, want nil", *got)
	}
	upsert(&n1)
	if got := nodeOf(); got == nil || *got != n1 {
		t.Fatalf("node_id = %v, want %s", got, n1)
	}
	if ids := mustGetNode(t, h, n1).WorkerIDs; !reflect.DeepEqual(ids, []string{"w1"}) {
		t.Fatalf("node view worker IDs = %v, want [w1]", ids)
	}
	upsert(&n2) // the latest registration wins
	if got := nodeOf(); got == nil || *got != n2 {
		t.Fatalf("node_id after moving = %v, want %s", got, n2)
	}
	if ids := mustGetNode(t, h, n1).WorkerIDs; len(ids) != 0 {
		t.Fatalf("worker still listed on the old node: %v", ids)
	}
	upsert(nil) // a registration without it resets to NULL
	if got := nodeOf(); got != nil {
		t.Fatalf("node_id after a registration without it = %q, want nil", *got)
	}
}

// RunWorkerUnknownNodeRejectedConformance: a worker registration that names an unregistered node is
// rejected with ErrNodeNotFound and stores nothing; a failed move leaves the worker on its node.
// (Mutant: existence check removed.)
func RunWorkerUnknownNodeRejectedConformance(t *testing.T, newHarness func(t *testing.T) NodeHarness) {
	h := newHarness(t)
	ctx := context.Background()
	known, unknown := NodeTestID(1), NodeTestID(9)
	mustRegister(t, h, nodeRegistration(known, "n", 100))

	w := &domain.Worker{ID: "w_new", Version: "v1", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &unknown}
	if err := h.Repo.UpsertWorker(ctx, w); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("UpsertWorker with an unregistered node: err = %v, want ErrNodeNotFound", err)
	}
	if _, err := h.Repo.GetWorker(ctx, "w_new"); !errors.Is(err, domain.ErrWorkerNotFound) {
		t.Fatalf("the rejected worker was stored: GetWorker err = %v", err)
	}

	ok := &domain.Worker{ID: "w_ok", Version: "v1", TotalSlots: 1, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &known}
	if err := h.Repo.UpsertWorker(ctx, ok); err != nil {
		t.Fatal(err)
	}
	move := &domain.Worker{ID: "w_ok", Version: "v2", TotalSlots: 3, Status: domain.WorkerStatusHealthy, SupportedTasks: []string{"echo"}, NodeID: &unknown}
	if err := h.Repo.UpsertWorker(ctx, move); !errors.Is(err, domain.ErrNodeNotFound) {
		t.Fatalf("moving to an unregistered node: err = %v, want ErrNodeNotFound", err)
	}
	got, err := h.Repo.GetWorker(ctx, "w_ok")
	if err != nil || got.NodeID == nil || *got.NodeID != known || got.Version != "v1" || got.TotalSlots != 1 {
		t.Fatalf("a rejected move changed the worker: %+v (err=%v)", got, err)
	}
}
