package platform

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

func fixture(name string) string { return filepath.Join("testdata", name) }

func ptrEq(t *testing.T, field string, got *int64, want *int64) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, deref(got), deref(want))
	case *got != *want:
		t.Errorf("%s = %d, want %d", field, *got, *want)
	}
}

func deref(p *int64) any {
	if p == nil {
		return "nil"
	}
	return *p
}

func i(v int64) *int64 { return &v }

func TestProbeLinux_ParsesFixtures(t *testing.T) {
	tests := []struct {
		name      string
		model     *string // nil = unknown
		logical   *int64
		physical  *int64
		memTotal  *int64
		memAvail  *int64
		loadMilli *int64
	}{
		{"full", strp("Test CPU Model X @ 3.00GHz"), i(4), i(2), i(16384000 * 1024), i(8192000 * 1024), i(850)},
		// kernels that omit the core keys (some ARM): physical and model are nil, not 0.
		{"nocorekeys", nil, i(2), nil, i(16384000 * 1024), i(8192000 * 1024), i(0)},
		// kernels without MemAvailable: nil, not 0.
		{"nomemavailable", strp("Test CPU Model X @ 3.00GHz"), i(4), i(2), i(16384000 * 1024), nil, i(1500)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newLinuxProbe(fixture(tc.name), "")
			h, err := p.Hardware(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if h.OS != runtime.GOOS || h.Arch != runtime.GOARCH {
				t.Errorf("os/arch = %s/%s", h.OS, h.Arch)
			}
			switch {
			case tc.model == nil && h.CPUModel != nil:
				t.Errorf("cpu_model = %q, want nil", *h.CPUModel)
			case tc.model != nil && (h.CPUModel == nil || *h.CPUModel != *tc.model):
				t.Errorf("cpu_model = %v, want %q", h.CPUModel, *tc.model)
			}
			ptrEq(t, "cpu_logical_cores", h.CPULogicalCores, tc.logical)
			ptrEq(t, "cpu_physical_cores", h.CPUPhysicalCores, tc.physical)
			ptrEq(t, "memory_total_bytes", h.MemoryTotalBytes, tc.memTotal)
			ptrEq(t, "disk_total_bytes (no state dir)", h.DiskTotalBytes, nil)
			if err := h.Validate(); err != nil {
				t.Errorf("probe hardware must satisfy the contract: %v", err)
			}

			tel, err := p.Telemetry(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ptrEq(t, "cpu_load_1m_milli", tel.CPULoad1mMilli, tc.loadMilli)
			ptrEq(t, "memory_available_bytes", tel.MemoryAvailableBytes, tc.memAvail)
			ptrEq(t, "disk_free_bytes (no state dir)", tel.DiskFreeBytes, nil)
			if err := tel.Validate(); err != nil {
				t.Errorf("probe telemetry must satisfy the contract: %v", err)
			}
		})
	}
}

func strp(s string) *string { return &s }

func TestProbeLinux_MissingFilesGiveNilNotError(t *testing.T) {
	p := newLinuxProbe(t.TempDir(), "") // empty root: nothing readable
	h, err := p.Hardware(context.Background())
	if err != nil {
		t.Fatalf("a probe error for a field must not fail the report: %v", err)
	}
	if h.CPUModel != nil || h.CPULogicalCores != nil || h.CPUPhysicalCores != nil || h.MemoryTotalBytes != nil {
		t.Fatalf("unreadable sources must be nil: %+v", h)
	}
	tel, err := p.Telemetry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tel != (domain.NodeTelemetry{}) {
		t.Fatalf("unreadable sources must be nil: %+v", tel)
	}
}

func TestProbeLinux_DiskFromStateDir(t *testing.T) {
	dir := t.TempDir()
	p := newLinuxProbe(fixture("full"), dir)
	h, err := p.Hardware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tel, err := p.Telemetry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.DiskTotalBytes == nil || *h.DiskTotalBytes <= 0 {
		t.Fatalf("disk_total_bytes = %v, want > 0 for a real directory", deref(h.DiskTotalBytes))
	}
	if tel.DiskFreeBytes == nil || *tel.DiskFreeBytes < 0 {
		t.Fatalf("disk_free_bytes = %v, want >= 0", deref(tel.DiskFreeBytes))
	}
	if *tel.DiskFreeBytes > *h.DiskTotalBytes {
		t.Fatalf("free %d exceeds total %d", *tel.DiskFreeBytes, *h.DiskTotalBytes)
	}

	// A state directory that does not exist leaves the disk fields unknown.
	p = newLinuxProbe(fixture("full"), filepath.Join(dir, "missing"))
	h, _ = p.Hardware(context.Background())
	if h.DiskTotalBytes != nil {
		t.Fatalf("missing state dir: disk_total_bytes = %d, want nil", *h.DiskTotalBytes)
	}
}

func TestProbeLinux_CgroupLowersMemory(t *testing.T) {
	const hostTotal = 16384000 * 1024
	tests := []struct {
		name      string
		wantTotal int64
		wantAvail int64
	}{
		// memory.max 4 GiB below MemTotal: total is lowered; available is lowered to
		// memory.max - memory.current = 3 GiB (below MemAvailable of about 8 GB).
		{"cgroup_low", 4294967296, 4294967296 - 1073741824},
		// memory.max is "max": nothing changes.
		{"cgroup_max", hostTotal, 8192000 * 1024},
		// memory.max above MemTotal never raises the total; headroom 31 GiB does not raise
		// available either.
		{"cgroup_high", hostTotal, 8192000 * 1024},
		// no cgroup files at all: nothing changes.
		{"full", hostTotal, 8192000 * 1024},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newLinuxProbe(fixture(tc.name), "")
			h, err := p.Hardware(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ptrEq(t, "memory_total_bytes", h.MemoryTotalBytes, i(tc.wantTotal))
			tel, err := p.Telemetry(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ptrEq(t, "memory_available_bytes", tel.MemoryAvailableBytes, i(tc.wantAvail))
		})
	}
}

func TestProbeLinux_CgroupHeadroomNeverNegative(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/meminfo", "MemTotal: 1000 kB\nMemAvailable: 900 kB\n")
	write("sys/fs/cgroup/memory.max", "100000\n")
	write("sys/fs/cgroup/memory.current", "200000\n") // current above max
	tel, err := newLinuxProbe(root, "").Telemetry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ptrEq(t, "memory_available_bytes", tel.MemoryAvailableBytes, i(0))
}

// TestProbeLinux_GPUFieldsAlwaysNil: the V2-1 Linux probe returns nil for every GPU field on
// every fixture, including one with a fake GPU entry under sysfs, and never 0 (owner answer Q4).
func TestProbeLinux_GPUFieldsAlwaysNil(t *testing.T) {
	for _, name := range []string{"full", "nocorekeys", "nomemavailable", "gpu", "cgroup_low", "cgroup_max", "cgroup_high"} {
		t.Run(name, func(t *testing.T) {
			h, err := newLinuxProbe(fixture(name), t.TempDir()).Hardware(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if h.GPUCount != nil {
				t.Errorf("gpu_count = %d, want nil (unknown), never 0", *h.GPUCount)
			}
			if h.GPUModel != nil {
				t.Errorf("gpu_model = %q, want nil", *h.GPUModel)
			}
			if h.GPUVRAMTotalBytes != nil {
				t.Errorf("gpu_vram_total_bytes = %d, want nil", *h.GPUVRAMTotalBytes)
			}
		})
	}
	// the gpu fixture really contains the fake sysfs entry the probe must ignore.
	if _, err := os.Stat(filepath.Join(fixture("gpu"), "sys/class/drm/card0/device/vendor")); err != nil {
		t.Fatalf("fixture lost its fake GPU entry: %v", err)
	}
}

func TestParseCPUInfo_Edges(t *testing.T) {
	t.Run("non-printable bytes in model are replaced", func(t *testing.T) {
		ci := parseCPUInfo("processor : 0\nmodel name : Intel\xc2\xa9 CPU\x1b[2J\n")
		if ci.model == nil || *ci.model != "Intel?? CPU?[2J" {
			t.Fatalf("model = %v", ci.model)
		}
		if err := (domain.NodeHardware{OS: "linux", Arch: "amd64", CPUModel: ci.model}).Validate(); err != nil {
			t.Fatalf("sanitized model must validate: %v", err)
		}
	})
	t.Run("long model is truncated to the contract maximum", func(t *testing.T) {
		ci := parseCPUInfo("processor : 0\nmodel name : " + strings.Repeat("a", 500) + "\n")
		if ci.model == nil || len(*ci.model) != domain.MaxModelBytes {
			t.Fatalf("model length = %v", ci.model)
		}
	})
	t.Run("empty model is nil", func(t *testing.T) {
		if ci := parseCPUInfo("processor : 0\nmodel name :    \n"); ci.model != nil {
			t.Fatalf("model = %q, want nil", *ci.model)
		}
	})
	t.Run("two sockets count distinct physical id and core id pairs", func(t *testing.T) {
		in := "processor: 0\nphysical id: 0\ncore id: 0\n\nprocessor: 1\nphysical id: 1\ncore id: 0\n\n" +
			"processor: 2\nphysical id: 0\ncore id: 0\n"
		ci := parseCPUInfo(in)
		ptrEq(t, "logical", ci.logical, i(3))
		ptrEq(t, "physical", ci.physical, i(2))
	})
	t.Run("only one of the two keys leaves physical nil", func(t *testing.T) {
		ci := parseCPUInfo("processor: 0\ncore id: 0\n\nprocessor: 1\ncore id: 1\n")
		ptrEq(t, "logical", ci.logical, i(2))
		ptrEq(t, "physical", ci.physical, nil)
	})
	t.Run("no processor lines leave logical nil", func(t *testing.T) {
		ci := parseCPUInfo("model name : X\n")
		ptrEq(t, "logical", ci.logical, nil)
	})
}

func TestParseLoadAvgMilli_Edges(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want *int64
	}{
		{"0.85 0.6 0.4 1/2 3", i(850)},
		{"0.00 0 0 1/2 3", i(0)},
		{"12.345 1 1 1/2 3", i(12345)},
		{"", nil},
		{"abc 1 1", nil},
		{"-1.0 1 1", nil},
		{"NaN 1 1", nil},
		{"Inf 1 1", nil},
		{"99999999999 1 1", nil},
	} {
		ptrEq(t, "loadavg "+tc.in, parseLoadAvgMilli(tc.in), tc.want)
	}
}

func TestMeminfoBytes_Edges(t *testing.T) {
	ptrEq(t, "kB", meminfoBytes("MemTotal: 10 kB\n", "MemTotal"), i(10240))
	ptrEq(t, "missing key", meminfoBytes("MemTotal: 10 kB\n", "MemAvailable"), nil)
	ptrEq(t, "garbage value", meminfoBytes("MemTotal: abc kB\n", "MemTotal"), nil)
	ptrEq(t, "unknown unit", meminfoBytes("MemTotal: 10 MB\n", "MemTotal"), nil)
	ptrEq(t, "no value", meminfoBytes("MemTotal:\n", "MemTotal"), nil)
	ptrEq(t, "overflows contract max", meminfoBytes("MemTotal: 18446744073709551615 kB\n", "MemTotal"), nil)
	ptrEq(t, "real zero stays zero", meminfoBytes("MemAvailable: 0 kB\n", "MemAvailable"), i(0))
}

func TestProbeLinux_HonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newLinuxProbe(fixture("full"), "")
	if _, err := p.Hardware(ctx); err == nil {
		t.Fatal("Hardware: want context error")
	}
	if _, err := p.Telemetry(ctx); err == nil {
		t.Fatal("Telemetry: want context error")
	}
}

func TestNewProbe_IsLinuxProbeOnLinux(t *testing.T) {
	if _, ok := NewProbe("").(*linuxProbe); !ok {
		t.Fatalf("NewProbe on linux must return the linux probe, got %T", NewProbe(""))
	}
}
