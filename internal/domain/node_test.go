package domain

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func i64(v int64) *int64         { return &v }
func str(v string) *string       { return &v }
func rep(s string, n int) string { return strings.Repeat(s, n) }

const goodUUID = "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23"

func TestNodeID_Validate_Table(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"valid", goodUUID, true},
		{"valid variant 8", "3f1c9a2e-6b7d-4e21-8c4a-0d5e8f7a1b23", true},
		{"valid variant 9", "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23", true},
		{"valid variant b", "3f1c9a2e-6b7d-4e21-bc4a-0d5e8f7a1b23", true},
		{"uppercase", strings.ToUpper(goodUUID), false},
		{"one uppercase hex digit", "3F1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b23", false},
		{"braces", "{" + goodUUID + "}", false},
		{"version 1", "3f1c9a2e-6b7d-1e21-9c4a-0d5e8f7a1b23", false},
		{"version 5", "3f1c9a2e-6b7d-5e21-9c4a-0d5e8f7a1b23", false},
		{"variant c", "3f1c9a2e-6b7d-4e21-cc4a-0d5e8f7a1b23", false},
		{"variant 7", "3f1c9a2e-6b7d-4e21-7c4a-0d5e8f7a1b23", false},
		{"35 bytes", goodUUID[:35], false},
		{"37 bytes", goodUUID + "a", false},
		{"empty", "", false},
		{"no hyphens", strings.ReplaceAll(goodUUID, "-", ""), false},
		{"hyphen moved", "3f1c9a2e6-b7d-4e21-9c4a-0d5e8f7a1b23", false},
		{"non hex", "3f1c9a2e-6b7d-4e21-9c4a-0d5e8f7a1b2g", false},
		{"trailing newline", goodUUID[:35] + "\n", false},
		{"nul byte", goodUUID[:35] + "\x00", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateNodeID(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("want error for %q", tc.in)
				}
				if !errors.Is(err, ErrInvalidRequest) {
					t.Fatalf("error does not wrap ErrInvalidRequest: %v", err)
				}
			}
		})
	}
}

func TestFormatNodeID_AlwaysValidV4(t *testing.T) {
	for _, b := range [][16]byte{
		{},
		{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
	} {
		id := FormatNodeID(b)
		if err := ValidateNodeID(id); err != nil {
			t.Fatalf("FormatNodeID(%v)=%q invalid: %v", b, id, err)
		}
	}
}

func TestNodeHardware_Validate_Table(t *testing.T) {
	base := func() NodeHardware { return NodeHardware{OS: "linux", Arch: "amd64"} }
	tests := []struct {
		name string
		mut  func(*NodeHardware)
		ok   bool
	}{
		{"all nil optional passes", func(h *NodeHardware) {}, true},
		{"os 32 bytes", func(h *NodeHardware) { h.OS = rep("a", 32) }, true},
		{"os 33 bytes", func(h *NodeHardware) { h.OS = rep("a", 33) }, false},
		{"os empty", func(h *NodeHardware) { h.OS = "" }, false},
		{"os uppercase", func(h *NodeHardware) { h.OS = "Linux" }, false},
		{"os hyphen", func(h *NodeHardware) { h.OS = "lin-ux" }, false},
		{"arch empty", func(h *NodeHardware) { h.Arch = "" }, false},
		{"arch 33 bytes", func(h *NodeHardware) { h.Arch = rep("a", 33) }, false},
		{"arch underscore digit", func(h *NodeHardware) { h.Arch = "arm_64" }, true},
		{"cpu_model 128 bytes", func(h *NodeHardware) { h.CPUModel = str(rep("a", 128)) }, true},
		{"cpu_model 129 bytes", func(h *NodeHardware) { h.CPUModel = str(rep("a", 129)) }, false},
		{"cpu_model empty", func(h *NodeHardware) { h.CPUModel = str("") }, false},
		{"cpu_model non-ascii", func(h *NodeHardware) { h.CPUModel = str("CPUé") }, false},
		{"cpu_model control", func(h *NodeHardware) { h.CPUModel = str("a\x1bb") }, false},
		{"cpu_model del", func(h *NodeHardware) { h.CPUModel = str("a\x7fb") }, false},
		{"gpu_model 129 bytes", func(h *NodeHardware) { h.GPUModel = str(rep("a", 129)) }, false},
		{"gpu_model 128 bytes", func(h *NodeHardware) { h.GPUModel = str(rep("a", 128)) }, true},
		{"logical cores max", func(h *NodeHardware) { h.CPULogicalCores = i64(MaxNodeCount) }, true},
		{"logical cores max+1", func(h *NodeHardware) { h.CPULogicalCores = i64(MaxNodeCount + 1) }, false},
		{"logical cores 0", func(h *NodeHardware) { h.CPULogicalCores = i64(0) }, false},
		{"logical cores negative", func(h *NodeHardware) { h.CPULogicalCores = i64(-1) }, false},
		{"physical cores 0", func(h *NodeHardware) { h.CPUPhysicalCores = i64(0) }, false},
		{"physical cores max+1", func(h *NodeHardware) { h.CPUPhysicalCores = i64(MaxNodeCount + 1) }, false},
		{"logical == physical", func(h *NodeHardware) { h.CPULogicalCores, h.CPUPhysicalCores = i64(4), i64(4) }, true},
		{"logical < physical", func(h *NodeHardware) { h.CPULogicalCores, h.CPUPhysicalCores = i64(3), i64(4) }, false},
		{"logical only, physical unknown", func(h *NodeHardware) { h.CPULogicalCores = i64(1) }, true},
		{"physical only, logical unknown", func(h *NodeHardware) { h.CPUPhysicalCores = i64(64) }, true},
		{"gpu_count 0 is a real value", func(h *NodeHardware) { h.GPUCount = i64(0) }, true},
		{"gpu_count max", func(h *NodeHardware) { h.GPUCount = i64(MaxNodeCount) }, true},
		{"gpu_count max+1", func(h *NodeHardware) { h.GPUCount = i64(MaxNodeCount + 1) }, false},
		{"gpu_count negative", func(h *NodeHardware) { h.GPUCount = i64(-1) }, false},
		{"memory_total 0", func(h *NodeHardware) { h.MemoryTotalBytes = i64(0) }, true},
		{"memory_total max", func(h *NodeHardware) { h.MemoryTotalBytes = i64(MaxNodeBytes) }, true},
		{"memory_total max+1", func(h *NodeHardware) { h.MemoryTotalBytes = i64(MaxNodeBytes + 1) }, false},
		{"memory_total negative", func(h *NodeHardware) { h.MemoryTotalBytes = i64(-1) }, false},
		{"disk_total max", func(h *NodeHardware) { h.DiskTotalBytes = i64(MaxNodeBytes) }, true},
		{"disk_total max+1", func(h *NodeHardware) { h.DiskTotalBytes = i64(MaxNodeBytes + 1) }, false},
		{"disk_total negative", func(h *NodeHardware) { h.DiskTotalBytes = i64(-1) }, false},
		{"gpu_vram max", func(h *NodeHardware) { h.GPUVRAMTotalBytes = i64(MaxNodeBytes) }, true},
		{"gpu_vram max+1", func(h *NodeHardware) { h.GPUVRAMTotalBytes = i64(MaxNodeBytes + 1) }, false},
		{"gpu_vram negative", func(h *NodeHardware) { h.GPUVRAMTotalBytes = i64(-1) }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := base()
			tc.mut(&h)
			err := h.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestNodeTelemetry_Validate_Table(t *testing.T) {
	base := func() NodeTelemetryReport {
		return NodeTelemetryReport{ContractVersion: ContractVersion, SessionID: goodUUID, Seq: 1}
	}
	tests := []struct {
		name string
		mut  func(*NodeTelemetryReport)
		ok   bool
	}{
		{"minimal passes, all nil", func(r *NodeTelemetryReport) {}, true},
		{"load max", func(r *NodeTelemetryReport) { r.CPULoad1mMilli = i64(MaxLoadMilli) }, true},
		{"load max+1", func(r *NodeTelemetryReport) { r.CPULoad1mMilli = i64(MaxLoadMilli + 1) }, false},
		{"load negative", func(r *NodeTelemetryReport) { r.CPULoad1mMilli = i64(-1) }, false},
		{"load 0", func(r *NodeTelemetryReport) { r.CPULoad1mMilli = i64(0) }, true},
		{"mem avail max", func(r *NodeTelemetryReport) { r.MemoryAvailableBytes = i64(MaxNodeBytes) }, true},
		{"mem avail max+1", func(r *NodeTelemetryReport) { r.MemoryAvailableBytes = i64(MaxNodeBytes + 1) }, false},
		{"mem avail negative", func(r *NodeTelemetryReport) { r.MemoryAvailableBytes = i64(-1) }, false},
		{"disk free max", func(r *NodeTelemetryReport) { r.DiskFreeBytes = i64(MaxNodeBytes) }, true},
		{"disk free max+1", func(r *NodeTelemetryReport) { r.DiskFreeBytes = i64(MaxNodeBytes + 1) }, false},
		{"disk free negative", func(r *NodeTelemetryReport) { r.DiskFreeBytes = i64(-1) }, false},
		{"sample_age 0", func(r *NodeTelemetryReport) { r.SampleAgeMS = 0 }, true},
		{"sample_age max", func(r *NodeTelemetryReport) { r.SampleAgeMS = MaxSampleAgeMS }, true},
		{"sample_age max+1", func(r *NodeTelemetryReport) { r.SampleAgeMS = MaxSampleAgeMS + 1 }, false},
		{"sample_age negative", func(r *NodeTelemetryReport) { r.SampleAgeMS = -1 }, false},
		{"seq min", func(r *NodeTelemetryReport) { r.Seq = MinTelemetrySeq }, true},
		{"seq 0", func(r *NodeTelemetryReport) { r.Seq = 0 }, false},
		{"seq max", func(r *NodeTelemetryReport) { r.Seq = MaxTelemetrySeq }, true},
		{"seq max+1", func(r *NodeTelemetryReport) { r.Seq = MaxTelemetrySeq + 1 }, false},
		{"seq negative", func(r *NodeTelemetryReport) { r.Seq = -5 }, false},
		{"session bad", func(r *NodeTelemetryReport) { r.SessionID = strings.ToUpper(goodUUID) }, false},
		{"session empty", func(r *NodeTelemetryReport) { r.SessionID = "" }, false},
		{"contract version 0", func(r *NodeTelemetryReport) { r.ContractVersion = 0 }, false},
		{"contract version 2", func(r *NodeTelemetryReport) { r.ContractVersion = 2 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mut(&r)
			err := r.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestNodeRegistration_Validate_Table(t *testing.T) {
	base := func() NodeRegistration {
		return NodeRegistration{NodeID: goodUUID, ContractVersion: ContractVersion,
			Hardware: NodeHardware{OS: "linux", Arch: "amd64"}}
	}
	tests := []struct {
		name string
		mut  func(*NodeRegistration)
		ok   bool
	}{
		{"minimal", func(r *NodeRegistration) {}, true},
		{"display_name 64 bytes", func(r *NodeRegistration) { r.DisplayName = rep("a", 64) }, true},
		{"display_name 65 bytes", func(r *NodeRegistration) { r.DisplayName = rep("a", 65) }, false},
		{"display_name utf8 multi-byte 64 bytes", func(r *NodeRegistration) { r.DisplayName = rep("é", 32) }, true},
		{"display_name utf8 multi-byte 66 bytes", func(r *NodeRegistration) { r.DisplayName = rep("é", 33) }, false},
		{"display_name invalid utf8", func(r *NodeRegistration) { r.DisplayName = "a\xffb" }, false},
		{"display_name NUL", func(r *NodeRegistration) { r.DisplayName = "a\x00b" }, false},
		{"display_name escape", func(r *NodeRegistration) { r.DisplayName = "a\x1b[2Jb" }, false},
		{"display_name newline", func(r *NodeRegistration) { r.DisplayName = "a\nb" }, false},
		{"agent_version 64 bytes", func(r *NodeRegistration) { r.AgentVersion = rep("v", 64) }, true},
		{"agent_version 65 bytes", func(r *NodeRegistration) { r.AgentVersion = rep("v", 65) }, false},
		{"agent_version NUL", func(r *NodeRegistration) { r.AgentVersion = "v\x00" }, false},
		{"bad node id", func(r *NodeRegistration) { r.NodeID = "x" }, false},
		{"unsupported contract version", func(r *NodeRegistration) { r.ContractVersion = 99 }, false},
		{"bad hardware", func(r *NodeRegistration) { r.Hardware.OS = "" }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.mut(&r)
			err := r.Validate()
			if tc.ok && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !tc.ok && err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestContractVersion_SupportedSet(t *testing.T) {
	if ContractVersion != 1 {
		t.Fatalf("ContractVersion = %d, ADR-0022 ships version 1", ContractVersion)
	}
	if !IsSupportedContractVersion(1) || IsSupportedContractVersion(0) || IsSupportedContractVersion(2) {
		t.Fatal("supported set must be exactly {1}")
	}
	got := SupportedContractVersions()
	got[0] = 99 // the returned slice is a copy
	if !IsSupportedContractVersion(1) {
		t.Fatal("mutating the returned slice changed the supported set")
	}
}

func TestMaxHardwareHistory_IsTen(t *testing.T) {
	if MaxHardwareHistory != 10 {
		t.Fatalf("MaxHardwareHistory = %d, owner answer Q1 fixes 10", MaxHardwareHistory)
	}
}

// TestNodeHardware_NilAndZeroStayDifferent pins ADR-0022 Section 10: nil is unknown, 0 is a
// real value, and neither the types nor JSON round trips may merge them.
func TestNodeHardware_NilAndZeroStayDifferent(t *testing.T) {
	unknown := NodeHardware{OS: "linux", Arch: "amd64"}
	zero := NodeHardware{OS: "linux", Arch: "amd64", GPUCount: i64(0)}
	if unknown.GPUCount != nil {
		t.Fatal("unknown gpu_count must be nil")
	}
	if zero.GPUCount == nil || *zero.GPUCount != 0 {
		t.Fatal("zero gpu_count must be a non-nil pointer to 0")
	}

	ub, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	zb, err := json.Marshal(zero)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ub), `"gpu_count":null`) {
		t.Fatalf("unknown must marshal as null: %s", ub)
	}
	if !strings.Contains(string(zb), `"gpu_count":0`) {
		t.Fatalf("zero must marshal as 0: %s", zb)
	}

	// absent key and explicit null both decode to nil; 0 decodes to a pointer to 0.
	var fromAbsent, fromNull, fromZero NodeHardware
	for _, c := range []struct {
		in  string
		out *NodeHardware
	}{
		{`{"os":"linux","arch":"amd64"}`, &fromAbsent},
		{`{"os":"linux","arch":"amd64","gpu_count":null}`, &fromNull},
		{`{"os":"linux","arch":"amd64","gpu_count":0}`, &fromZero},
	} {
		if err := json.Unmarshal([]byte(c.in), c.out); err != nil {
			t.Fatal(err)
		}
	}
	if fromAbsent.GPUCount != nil || fromNull.GPUCount != nil {
		t.Fatal("absent and null must decode to nil (unknown)")
	}
	if fromZero.GPUCount == nil || *fromZero.GPUCount != 0 {
		t.Fatal("0 must decode to a non-nil zero")
	}

	// the same holds for telemetry.
	var tel NodeTelemetry
	if err := json.Unmarshal([]byte(`{"cpu_load_1m_milli":0}`), &tel); err != nil {
		t.Fatal(err)
	}
	if tel.CPULoad1mMilli == nil || *tel.CPULoad1mMilli != 0 || tel.DiskFreeBytes != nil {
		t.Fatal("telemetry: 0 must stay 0 and an absent key must stay nil")
	}
}

func TestNodeTelemetryReport_JSONIsFlat(t *testing.T) {
	var r NodeTelemetryReport
	in := `{"contract_version":1,"session_id":"` + goodUUID + `","seq":17,"sample_age_ms":120,` +
		`"cpu_load_1m_milli":850,"memory_available_bytes":9126805504,"disk_free_bytes":null}`
	if err := json.Unmarshal([]byte(in), &r); err != nil {
		t.Fatal(err)
	}
	if r.Seq != 17 || r.SampleAgeMS != 120 || r.CPULoad1mMilli == nil || *r.CPULoad1mMilli != 850 || r.DiskFreeBytes != nil {
		t.Fatalf("unexpected decode: %+v", r)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}
