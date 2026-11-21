package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func fullHardware() NodeHardware {
	return NodeHardware{
		OS: "linux", Arch: "amd64",
		CPUModel: str("Test CPU 9000"), CPULogicalCores: i64(8), CPUPhysicalCores: i64(4),
		MemoryTotalBytes: i64(17179869184), DiskTotalBytes: i64(512110190592),
		GPUCount: i64(0), GPUModel: str("Test GPU"), GPUVRAMTotalBytes: i64(8589934592),
	}
}

// TestDomain_NodeHardwareContentHash pins the canonical encoding of clarification K1. The golden
// values were computed by an independent implementation of the encoding (a Python script that
// builds the bytes from the K1 text), not by this code.
func TestDomain_NodeHardwareContentHash(t *testing.T) {
	const goldenFull = "6f699dd78ccb6f85971ccd28b0ac8d45f4380054ec9eb5a5b86d90bb0663a5a9"
	const goldenMin = "3685802d26370bc91a5431108177f1cd9359d4835d68b01742925caa0703621a"
	const goldenGPU0 = "ecdec599db592ba340184c1df23cbb714d41d88efcf2aacf7ad6a2433bf61550"
	const goldenABc = "19a8f4be1401c06613a2026232d7e9d6354b2363ece0c5f7779190c672d0ca35"
	const goldenAbc = "868bfc2f04f393ab38114ee71f9ea4c9fcefd1f62f5d7158f0bcc46e5456743d"

	min := NodeHardware{OS: "linux", Arch: "amd64"}
	gpu0 := NodeHardware{OS: "linux", Arch: "amd64", GPUCount: i64(0)}
	ab := NodeHardware{OS: "linux", Arch: "amd64", CPUModel: str("ab"), GPUModel: str("c")}
	a := NodeHardware{OS: "linux", Arch: "amd64", CPUModel: str("a"), GPUModel: str("bc")}

	t.Run("golden", func(t *testing.T) {
		for name, c := range map[string]struct {
			h    NodeHardware
			want string
		}{"full": {fullHardware(), goldenFull}, "minimal": {min, goldenMin}, "gpu0": {gpu0, goldenGPU0}, "ab_c": {ab, goldenABc}, "a_bc": {a, goldenAbc}} {
			if got := c.h.ContentHash(); got != c.want {
				t.Errorf("%s: ContentHash = %s, want %s", name, got, c.want)
			}
		}
		if h := fullHardware().ContentHash(); len(h) != 64 {
			t.Errorf("hash length = %d, want 64", len(h))
		}
	})

	t.Run("nil_versus_zero", func(t *testing.T) {
		fields := map[string]func(h *NodeHardware, zero bool){
			"cpu_logical_cores":    func(h *NodeHardware, z bool) { h.CPULogicalCores = pick(z) },
			"cpu_physical_cores":   func(h *NodeHardware, z bool) { h.CPUPhysicalCores = pick(z) },
			"memory_total_bytes":   func(h *NodeHardware, z bool) { h.MemoryTotalBytes = pick(z) },
			"disk_total_bytes":     func(h *NodeHardware, z bool) { h.DiskTotalBytes = pick(z) },
			"gpu_count":            func(h *NodeHardware, z bool) { h.GPUCount = pick(z) },
			"gpu_vram_total_bytes": func(h *NodeHardware, z bool) { h.GPUVRAMTotalBytes = pick(z) },
		}
		for name, set := range fields {
			a, b := min, min
			set(&a, false)
			set(&b, true)
			if a.ContentHash() == b.ContentHash() {
				t.Errorf("%s: nil and 0 hash the same", name)
			}
		}
		if min.ContentHash() == gpu0.ContentHash() {
			t.Error("gpu_count nil and 0 hash the same")
		}
	})

	t.Run("absent_string_versus_empty_string", func(t *testing.T) {
		absent := min
		empty := min
		empty.CPUModel = str("")
		if absent.ContentHash() == empty.ContentHash() {
			t.Error("absent cpu_model and empty cpu_model hash the same")
		}
		emptyGPU := min
		emptyGPU.GPUModel = str("")
		if absent.ContentHash() == emptyGPU.ContentHash() {
			t.Error("absent gpu_model and empty gpu_model hash the same")
		}
	})

	t.Run("ambiguous_concatenation", func(t *testing.T) {
		if ab.ContentHash() == a.ContentHash() {
			t.Fatal(`cpu_model "ab" + gpu_model "c" hashes like "a" + "bc": the encoding is ambiguous`)
		}
	})

	t.Run("field_order_stability", func(t *testing.T) {
		// Swapping the values of two same-typed fields must change the hash: the hash depends on
		// the fixed field order, not only on the multiset of values.
		x := min
		x.CPULogicalCores, x.CPUPhysicalCores = i64(8), i64(4)
		y := min
		y.CPULogicalCores, y.CPUPhysicalCores = i64(4), i64(8)
		if x.ContentHash() == y.ContentHash() {
			t.Error("swapping cpu_logical_cores and cpu_physical_cores did not change the hash")
		}
		m := min
		m.MemoryTotalBytes, m.DiskTotalBytes = i64(1), i64(2)
		n := min
		n.MemoryTotalBytes, n.DiskTotalBytes = i64(2), i64(1)
		if m.ContentHash() == n.ContentHash() {
			t.Error("swapping memory_total_bytes and disk_total_bytes did not change the hash")
		}
		o := NodeHardware{OS: "arm64", Arch: "linux"}
		if o.ContentHash() == (NodeHardware{OS: "linux", Arch: "arm64"}).ContentHash() {
			t.Error("swapping os and arch did not change the hash")
		}
		// Repeated calls and copies are stable.
		h := fullHardware()
		if h.ContentHash() != fullHardware().ContentHash() || h.ContentHash() != h.ContentHash() {
			t.Error("hash is not deterministic")
		}
	})

	t.Run("every_field_changes_the_hash", func(t *testing.T) {
		base := fullHardware().ContentHash()
		mut := map[string]func(h *NodeHardware){
			"os":                   func(h *NodeHardware) { h.OS = "darwin" },
			"arch":                 func(h *NodeHardware) { h.Arch = "arm64" },
			"cpu_model":            func(h *NodeHardware) { h.CPUModel = str("other") },
			"cpu_logical_cores":    func(h *NodeHardware) { h.CPULogicalCores = i64(16) },
			"cpu_physical_cores":   func(h *NodeHardware) { h.CPUPhysicalCores = i64(8) },
			"memory_total_bytes":   func(h *NodeHardware) { h.MemoryTotalBytes = i64(1) },
			"disk_total_bytes":     func(h *NodeHardware) { h.DiskTotalBytes = i64(1) },
			"gpu_count":            func(h *NodeHardware) { h.GPUCount = i64(1) },
			"gpu_model":            func(h *NodeHardware) { h.GPUModel = str("other") },
			"gpu_vram_total_bytes": func(h *NodeHardware) { h.GPUVRAMTotalBytes = i64(1) },
		}
		for name, f := range mut {
			h := fullHardware()
			f(&h)
			if h.ContentHash() == base {
				t.Errorf("changing %s did not change the hash", name)
			}
		}
	})

	t.Run("separator_version_changes_the_hash", func(t *testing.T) {
		h := fullHardware()
		if got := h.contentHashWithDomain(hardwareHashDomain); got != h.ContentHash() {
			t.Fatalf("contentHashWithDomain(const) = %s, ContentHash = %s", got, h.ContentHash())
		}
		if h.contentHashWithDomain("minicloud.node.hardware.v2") == h.ContentHash() {
			t.Error("a different separator version string did not change the hash")
		}
		if h.contentHashWithDomain("") == h.ContentHash() {
			t.Error("an empty separator did not change the hash")
		}
	})
}

func pick(zero bool) *int64 {
	if zero {
		return i64(0)
	}
	return nil
}

// TestDomain_NodeHardwareContentHash_VersionsNotHashed: agent_version and contract_version are
// not hardware and do not enter the hash. They are not fields of NodeHardware at all, so the
// registration carrying a different agent version hashes its hardware identically.
func TestDomain_NodeHardwareContentHash_VersionsNotHashed(t *testing.T) {
	a := NodeRegistration{AgentVersion: "v1", ContractVersion: 1, Hardware: fullHardware()}
	b := NodeRegistration{AgentVersion: "v2", ContractVersion: 2, Hardware: fullHardware()}
	if a.Hardware.ContentHash() != b.Hardware.ContentHash() {
		t.Fatal("agent_version or contract_version changed the hardware hash")
	}
}

// TestDomain_HardwareUpdateLimit is the table of clarification K3, including the three boundary
// cases to the nanosecond (elapsed == interval - 1ns, == interval, == interval + 1ns).
func TestDomain_HardwareUpdateLimit(t *testing.T) {
	const iv = 60 * time.Second
	base := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name        string
		elapsed     time.Duration
		interval    time.Duration
		wantLimited bool
		wantRetry   time.Duration
	}{
		{"zero elapsed", 0, iv, true, 60 * time.Second},
		{"one nanosecond elapsed rounds up to the full interval", 1, iv, true, 60 * time.Second},
		{"one second elapsed", time.Second, iv, true, 59 * time.Second},
		{"half a second elapsed rounds 59.5 up to 60", 500 * time.Millisecond, iv, true, 60 * time.Second},
		{"59.5 s elapsed rounds 0.5 up to 1", 59*time.Second + 500*time.Millisecond, iv, true, time.Second},
		{"boundary minus one nanosecond", iv - 1, iv, true, time.Second},
		{"boundary exactly", iv, iv, false, 0},
		{"boundary plus one nanosecond", iv + 1, iv, false, 0},
		{"long after", time.Hour, iv, false, 0},
		{"clock moved back one second", -time.Second, iv, true, 60 * time.Second},
		{"clock moved back one nanosecond", -1, iv, true, 60 * time.Second},
		{"clock moved back by a lot", -24 * time.Hour, iv, true, 60 * time.Second},
		{"non whole interval rounds up when clock moved back", -time.Second, 90*time.Second + time.Nanosecond, true, 91 * time.Second},
		{"sub-second interval, limited, never below one second", 0, 100 * time.Millisecond, true, time.Second},
		{"sub-second interval, boundary accepted", 100 * time.Millisecond, 100 * time.Millisecond, false, 0},
		// Non-positive intervals are not valid configuration, but the function must stay total: a
		// clock that moved backwards is always limited, and a limit is never below one second.
		{"zero interval accepts zero elapsed", 0, 0, false, 0},
		{"zero interval, clock moved back one nanosecond, limited with the one second floor", -1, 0, true, time.Second},
		{"negative interval, clock moved back, still limited", -time.Second, -time.Hour, true, time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, limited := HardwareUpdateLimit(base, base.Add(c.elapsed), c.interval)
			if limited != c.wantLimited || got != c.wantRetry {
				t.Fatalf("HardwareUpdateLimit(elapsed=%v, interval=%v) = (%v, %v), want (%v, %v)", c.elapsed, c.interval, got, limited, c.wantRetry, c.wantLimited)
			}
			if limited && (got < time.Second || got%time.Second != 0) {
				t.Fatalf("retryAfter %v is not a whole number of seconds >= 1", got)
			}
		})
	}
}

func TestDomain_NodeRateLimitedError(t *testing.T) {
	var err error = &NodeRateLimitedError{RetryAfter: 42 * time.Second}
	if !errors.Is(err, ErrNodeRateLimited) {
		t.Fatal("NodeRateLimitedError does not match ErrNodeRateLimited")
	}
	wrapped := fmt.Errorf("register: %w", err)
	var rl *NodeRateLimitedError
	if !errors.As(wrapped, &rl) || rl.RetryAfterSeconds() != 42 {
		t.Fatalf("errors.As / RetryAfterSeconds failed: %v", wrapped)
	}
	if errors.Is(err, ErrNodeNotFound) || errors.Is(err, ErrTelemetryOutOfOrder) {
		t.Fatal("rate limit error matches an unrelated sentinel")
	}
}

// TestDomain_WorkerJSON_LegacyUnchanged: a legacy worker (NodeID nil) marshals to exactly the
// bytes the V1 struct produced, with no node_id key; a worker with a node marshals the key.
func TestDomain_WorkerJSON_LegacyUnchanged(t *testing.T) {
	at := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	w := Worker{
		ID: "w1", Version: "v1.0.0", Status: WorkerStatusHealthy, TotalSlots: 4, AvailableSlots: 3,
		SupportedTasks: []string{"echo"}, LastHeartbeatAt: at, RegisteredAt: at, ExecutesConcurrently: true,
	}
	got, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	// Written out by hand from the V1 field list and tags (origin/main before V2-1b).
	const want = `{"id":"w1","version":"v1.0.0","status":"HEALTHY","total_slots":4,"available_slots":3,"supported_tasks":["echo"],"last_heartbeat_at":"2026-11-21T12:00:00Z","registered_at":"2026-11-21T12:00:00Z","executes_concurrently":true}`
	if string(got) != want {
		t.Fatalf("legacy worker JSON changed:\n got %s\nwant %s", got, want)
	}
	node := "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23"
	w.NodeID = &node
	got, _ = json.Marshal(w)
	wantNode := want[:len(want)-1] + `,"node_id":"` + node + `"}`
	if string(got) != wantNode {
		t.Fatalf("node worker JSON:\n got %s\nwant %s", got, wantNode)
	}
}
