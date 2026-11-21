//go:build linux

package platform

import (
	"bufio"
	"context"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// linuxProbe reads the sources of ADR-0022 Section 6.3 with the standard library only. All
// paths are resolved under root so tests point it at fixtures and never read the real /proc.
// In production root is "/".
type linuxProbe struct {
	root     string // filesystem root: <root>/proc/..., <root>/sys/fs/cgroup/...
	stateDir string // statfs target for the disk fields; "" means unknown
}

// NewProbe returns the Linux probe. stateDir is the directory whose volume is measured for
// disk_total_bytes and disk_free_bytes (ADR-0022 Section 3.2); "" leaves them unknown.
func NewProbe(stateDir string) Probe { return newLinuxProbe("/", stateDir) }

func newLinuxProbe(root, stateDir string) *linuxProbe {
	return &linuxProbe{root: root, stateDir: stateDir}
}

func (p *linuxProbe) path(rel string) string { return filepath.Join(p.root, rel) }

// readFile returns the content of a file under root, or "" and false when it is unreadable.
// A probe error for one field makes that field nil; it never fails the whole report.
func (p *linuxProbe) readFile(rel string) (string, bool) {
	b, err := os.ReadFile(p.path(rel))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// Hardware implements Probe. GPU fields are ALWAYS nil in V2-1 (owner answer Q4): the probe
// never reads sysfs for GPUs and never reports gpu_count 0.
func (p *linuxProbe) Hardware(ctx context.Context) (domain.NodeHardware, error) {
	if err := ctx.Err(); err != nil {
		return domain.NodeHardware{}, err
	}
	h := domain.NodeHardware{OS: runtime.GOOS, Arch: runtime.GOARCH}

	if s, ok := p.readFile("proc/cpuinfo"); ok {
		ci := parseCPUInfo(s)
		h.CPUModel, h.CPULogicalCores, h.CPUPhysicalCores = ci.model, ci.logical, ci.physical
	}

	if s, ok := p.readFile("proc/meminfo"); ok {
		h.MemoryTotalBytes = meminfoBytes(s, "MemTotal")
	}
	if limit, ok := p.cgroupMemoryMax(); ok && h.MemoryTotalBytes != nil && limit < *h.MemoryTotalBytes {
		v := limit
		h.MemoryTotalBytes = &v
	}

	h.DiskTotalBytes, _ = p.disk()
	return h, nil
}

// Telemetry implements Probe.
func (p *linuxProbe) Telemetry(ctx context.Context) (domain.NodeTelemetry, error) {
	if err := ctx.Err(); err != nil {
		return domain.NodeTelemetry{}, err
	}
	var t domain.NodeTelemetry

	if s, ok := p.readFile("proc/loadavg"); ok {
		t.CPULoad1mMilli = parseLoadAvgMilli(s)
	}

	if s, ok := p.readFile("proc/meminfo"); ok {
		t.MemoryAvailableBytes = meminfoBytes(s, "MemAvailable")
	}
	if t.MemoryAvailableBytes != nil {
		if head, ok := p.cgroupMemoryHeadroom(); ok && head < *t.MemoryAvailableBytes {
			v := head
			t.MemoryAvailableBytes = &v
		}
	}

	_, t.DiskFreeBytes = p.disk()
	return t, nil
}

// Only the cgroup v2 root files /sys/fs/cgroup/memory.max and memory.current are read, so a
// host process inside a nested cgroup (for example a systemd service slice) is not scoped:
// threat T8 is partly covered. Reading /proc/self/cgroup to find the process's own cgroup is a
// later improvement.

// cgroupMemoryMax returns the cgroup v2 memory.max limit when the file is readable and holds
// a number ("max" means no limit).
func (p *linuxProbe) cgroupMemoryMax() (int64, bool) {
	s, ok := p.readFile("sys/fs/cgroup/memory.max")
	if !ok {
		return 0, false
	}
	return parseCgroupNumber(s)
}

// cgroupMemoryHeadroom returns memory.max - memory.current (never negative) when both are
// readable and memory.max is a number.
func (p *linuxProbe) cgroupMemoryHeadroom() (int64, bool) {
	limit, ok := p.cgroupMemoryMax()
	if !ok {
		return 0, false
	}
	s, ok := p.readFile("sys/fs/cgroup/memory.current")
	if !ok {
		return 0, false
	}
	cur, ok := parseCgroupNumber(s)
	if !ok {
		return 0, false
	}
	if cur >= limit {
		return 0, true
	}
	return limit - cur, true
}

// disk returns total and available bytes of the volume holding stateDir, or nil, nil.
func (p *linuxProbe) disk() (total, free *int64) {
	if p.stateDir == "" {
		return nil, nil
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(p.stateDir, &st); err != nil {
		return nil, nil
	}
	bsize := uint64(st.Bsize)
	return capBytes(uint64(st.Blocks), bsize), capBytes(uint64(st.Bavail), bsize)
}

// capBytes returns n*size, or nil when it overflows or exceeds the contract maximum.
func capBytes(n, size uint64) *int64 {
	if size != 0 && n > math.MaxInt64/size {
		return nil
	}
	v := n * size
	if v > domain.MaxNodeBytes {
		return nil
	}
	r := int64(v)
	return &r
}

// cpuInfo is the parsed result of /proc/cpuinfo; nil fields are unknown.
type cpuInfo struct {
	model    *string
	logical  *int64
	physical *int64
}

// parseCPUInfo reads the blocks of /proc/cpuinfo. Logical cores are the "processor" entries.
// Physical cores are the distinct (physical id, core id) pairs, counted only for blocks that
// carry both keys (some ARM kernels omit them), so a file without them leaves physical nil,
// never 0. Every pair comes from a processor block, so physical never exceeds logical.
func parseCPUInfo(s string) cpuInfo {
	var ci cpuInfo
	var logical int64
	pairs := map[string]struct{}{}
	var phys, core string
	var havePhys, haveCore, inBlock bool

	flush := func() {
		if inBlock {
			logical++
			if havePhys && haveCore {
				pairs[phys+"/"+core] = struct{}{}
			}
		}
		phys, core = "", ""
		havePhys, haveCore, inBlock = false, false, false
	}

	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			flush()
			inBlock = true
		case "model name":
			if ci.model == nil {
				ci.model = sanitizeModel(v)
			}
		case "physical id":
			phys, havePhys = v, true
		case "core id":
			core, haveCore = v, true
		}
	}
	flush()

	if logical > 0 && logical <= domain.MaxNodeCount {
		l := logical
		ci.logical = &l
	}
	if n := int64(len(pairs)); n > 0 {
		ci.physical = &n
	}
	return ci
}

// sanitizeModel replaces every byte outside printable ASCII with '?', trims, truncates to the
// contract maximum, and returns nil for an empty result (ADR-0022 Section 9: the adapter
// replaces other bytes).
func sanitizeModel(v string) *string {
	b := []byte(strings.TrimSpace(v))
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	if len(b) > domain.MaxModelBytes {
		b = b[:domain.MaxModelBytes]
	}
	if len(b) == 0 {
		return nil
	}
	s := string(b)
	return &s
}

// meminfoBytes returns the kB value of key in /proc/meminfo as bytes, or nil when the key is
// missing, unparsable or out of range.
func meminfoBytes(s, key string) *int64 {
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		fields := strings.Fields(v)
		if len(fields) == 0 {
			return nil
		}
		n, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			return nil
		}
		mult := uint64(1)
		if len(fields) > 1 {
			if strings.EqualFold(fields[1], "kB") {
				mult = 1024
			} else {
				return nil
			}
		}
		return capBytes(n, mult)
	}
	return nil
}

// parseLoadAvgMilli converts the first field of /proc/loadavg to milli-load, or nil.
func parseLoadAvgMilli(s string) *int64 {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return nil
	}
	m := math.Round(f * 1000)
	if m > domain.MaxLoadMilli {
		return nil
	}
	v := int64(m)
	return &v
}

// parseCgroupNumber parses a cgroup v2 value; "max" and anything else non numeric is not ok.
func parseCgroupNumber(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "max" {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > domain.MaxNodeBytes {
		return 0, false
	}
	return int64(n), true
}
