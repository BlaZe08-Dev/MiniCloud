package domain

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// This file holds the node capability contract of ADR-0022 (slice V2-1a): the hardware and
// telemetry types, the contract version, and every limit of ADR-0022 Section 9 with its
// validation. It contains no OS code and no import of internal/platform.
//
// Unknown versus zero (ADR-0022 Section 10): every measurement is a pointer. nil means
// unknown and a pointer to 0 is a real measured zero. A probe that cannot measure a field
// returns nil, never 0.

// ContractVersion is the node capability contract version this build speaks (ADR-0022
// Section 6.1). Changing the meaning or unit of a field needs a new version.
const ContractVersion = 1

// supportedContractVersions is the set of versions the coordinator accepts (V2-1: {1}).
var supportedContractVersions = []int{ContractVersion}

// SupportedContractVersions returns a copy of the accepted contract versions.
func SupportedContractVersions() []int {
	return append([]int(nil), supportedContractVersions...)
}

// IsSupportedContractVersion reports whether v is in the supported set. An unknown required
// version must fail closed (ADR-0022 Section 4.1).
func IsSupportedContractVersion(v int) bool {
	for _, s := range supportedContractVersions {
		if s == v {
			return true
		}
	}
	return false
}

// Limits of ADR-0022 Section 9. Byte and count caps are derived, not invented: 2^53 - 1 is the
// largest integer a JSON number carries exactly.
const (
	// NodeIDLength is the byte length of a canonical UUID string (also session_id).
	NodeIDLength = 36
	// MaxDisplayNameBytes caps display_name.
	MaxDisplayNameBytes = 64
	// MaxAgentVersionBytes caps agent_version (as workers.version, SEC-016).
	MaxAgentVersionBytes = 64
	// MaxOSArchBytes caps os and arch.
	MaxOSArchBytes = 32
	// MaxModelBytes caps cpu_model and gpu_model.
	MaxModelBytes = 128
	// MaxNodeCount is the upper bound of core and GPU counts (INTEGER).
	MaxNodeCount = 1<<31 - 1
	// MaxNodeBytes is the upper bound of every byte-valued field (2^53 - 1).
	MaxNodeBytes = 1<<53 - 1
	// MaxLoadMilli is the upper bound of cpu_load_1m_milli (INTEGER).
	MaxLoadMilli = 1<<31 - 1
	// MaxSampleAgeMS is the upper bound of sample_age_ms.
	MaxSampleAgeMS = 1<<31 - 1
	// MinTelemetrySeq and MaxTelemetrySeq bound seq.
	MinTelemetrySeq = 1
	MaxTelemetrySeq = 1<<53 - 1
	// MaxHardwareHistory is the number of hardware versions kept per node (owner answer Q1).
	// It is a constant, not a setting, because the owner fixed it.
	MaxHardwareHistory = 10
)

// NodeHardware is the static inventory of one node (ADR-0022 Section 3.2). OS and Arch are
// always known (runtime.GOOS and runtime.GOARCH); every other field is nil when unknown.
type NodeHardware struct {
	OS                string  `json:"os"`
	Arch              string  `json:"arch"`
	CPUModel          *string `json:"cpu_model"`
	CPULogicalCores   *int64  `json:"cpu_logical_cores"`
	CPUPhysicalCores  *int64  `json:"cpu_physical_cores"`
	MemoryTotalBytes  *int64  `json:"memory_total_bytes"`
	DiskTotalBytes    *int64  `json:"disk_total_bytes"`
	GPUCount          *int64  `json:"gpu_count"`
	GPUModel          *string `json:"gpu_model"`
	GPUVRAMTotalBytes *int64  `json:"gpu_vram_total_bytes"`
}

// NodeTelemetry is the latest dynamic sample of one node (ADR-0022 Section 3.3). Every field
// is nil when unknown.
type NodeTelemetry struct {
	CPULoad1mMilli       *int64 `json:"cpu_load_1m_milli"`
	MemoryAvailableBytes *int64 `json:"memory_available_bytes"`
	DiskFreeBytes        *int64 `json:"disk_free_bytes"`
}

// NodeRegistration is the body of the node registration report (ADR-0022 Section 4.1).
type NodeRegistration struct {
	NodeID          string       `json:"node_id"`
	DisplayName     string       `json:"display_name"`
	ContractVersion int          `json:"contract_version"`
	AgentVersion    string       `json:"agent_version"`
	Hardware        NodeHardware `json:"hardware"`
}

// NodeTelemetryReport is the body of a telemetry report (ADR-0022 Section 4.2). The embedded
// NodeTelemetry flattens into the same JSON object.
type NodeTelemetryReport struct {
	ContractVersion int    `json:"contract_version"`
	SessionID       string `json:"session_id"`
	Seq             int64  `json:"seq"`
	SampleAgeMS     int64  `json:"sample_age_ms"`
	NodeTelemetry
}

// invalidNode wraps ErrInvalidRequest so callers can use errors.Is.
func invalidNode(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRequest, fmt.Sprintf(format, args...))
}

// FormatNodeID formats 16 random bytes as a lowercase RFC 9562 version 4 UUID. It sets the
// version and variant bits on its own copy of b; it does not read randomness itself, so the
// domain stays free of OS code (the caller supplies crypto/rand bytes).
func FormatNodeID(b [16]byte) string {
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, NodeIDLength)
	for i, c := range b {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[c>>4], hexdigits[c&0x0f])
	}
	return string(out)
}

// ValidateNodeID accepts only a lowercase RFC 9562 version 4 UUID in canonical form: 36 bytes,
// hyphens at 8, 13, 18 and 23, version nibble 4, variant nibble one of 8, 9, a, b. The same
// rule validates session_id.
func ValidateNodeID(s string) error {
	if len(s) != NodeIDLength {
		return invalidNode("node id must be %d bytes, got %d", NodeIDLength, len(s))
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return invalidNode("node id: expected '-' at position %d", i)
			}
		case 14:
			if c != '4' {
				return invalidNode("node id: version nibble must be 4")
			}
		case 19:
			if c != '8' && c != '9' && c != 'a' && c != 'b' {
				return invalidNode("node id: variant nibble must be one of 8, 9, a, b")
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return invalidNode("node id: position %d is not a lowercase hex digit", i)
			}
		}
	}
	return nil
}

// ValidateSessionID validates a telemetry session_id (same format as a node ID).
func ValidateSessionID(s string) error {
	if err := ValidateNodeID(s); err != nil {
		return invalidNode("session_id: %v", err)
	}
	return nil
}

// ValidateDisplayName checks valid UTF-8, no control characters (NUL included), at most
// MaxDisplayNameBytes. The empty string is allowed.
func ValidateDisplayName(s string) error {
	if len(s) > MaxDisplayNameBytes {
		return invalidNode("display_name exceeds %d bytes", MaxDisplayNameBytes)
	}
	if !utf8.ValidString(s) {
		return invalidNode("display_name is not valid UTF-8")
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return invalidNode("display_name contains a control character")
	}
	return nil
}

// ValidateAgentVersion checks at most MaxAgentVersionBytes and no NUL.
func ValidateAgentVersion(s string) error {
	if len(s) > MaxAgentVersionBytes {
		return invalidNode("agent_version exceeds %d bytes", MaxAgentVersionBytes)
	}
	if strings.IndexByte(s, 0) >= 0 {
		return invalidNode("agent_version contains NUL")
	}
	return nil
}

// ValidateContractVersion fails closed on a version outside the supported set.
func ValidateContractVersion(v int) error {
	if !IsSupportedContractVersion(v) {
		return invalidNode("unsupported contract_version %d", v)
	}
	return nil
}

// validateOSArch checks ^[a-z0-9_]{1,32}$.
func validateOSArch(name, s string) error {
	if len(s) < 1 || len(s) > MaxOSArchBytes {
		return invalidNode("%s must be 1..%d bytes", name, MaxOSArchBytes)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '_') {
			return invalidNode("%s must match [a-z0-9_]", name)
		}
	}
	return nil
}

// validateModel checks nil or 1..MaxModelBytes of printable ASCII (0x20..0x7e).
func validateModel(name string, s *string) error {
	if s == nil {
		return nil
	}
	if len(*s) < 1 || len(*s) > MaxModelBytes {
		return invalidNode("%s must be 1..%d bytes", name, MaxModelBytes)
	}
	for i := 0; i < len(*s); i++ {
		if c := (*s)[i]; c < 0x20 || c > 0x7e {
			return invalidNode("%s must be printable ASCII", name)
		}
	}
	return nil
}

// validateRange checks nil or min..max.
func validateRange(name string, v *int64, min, max int64) error {
	if v == nil {
		return nil
	}
	if *v < min || *v > max {
		return invalidNode("%s must be %d..%d", name, min, max)
	}
	return nil
}

// Validate checks every limit of ADR-0022 Section 9 for the hardware fields. nil passes.
func (h NodeHardware) Validate() error {
	if err := validateOSArch("os", h.OS); err != nil {
		return err
	}
	if err := validateOSArch("arch", h.Arch); err != nil {
		return err
	}
	if err := validateModel("cpu_model", h.CPUModel); err != nil {
		return err
	}
	if err := validateModel("gpu_model", h.GPUModel); err != nil {
		return err
	}
	// cpu_* cores are at least 1 when known; gpu_count may be 0.
	if err := validateRange("cpu_logical_cores", h.CPULogicalCores, 1, MaxNodeCount); err != nil {
		return err
	}
	if err := validateRange("cpu_physical_cores", h.CPUPhysicalCores, 1, MaxNodeCount); err != nil {
		return err
	}
	if err := validateRange("gpu_count", h.GPUCount, 0, MaxNodeCount); err != nil {
		return err
	}
	if h.CPULogicalCores != nil && h.CPUPhysicalCores != nil && *h.CPULogicalCores < *h.CPUPhysicalCores {
		return invalidNode("cpu_logical_cores must be >= cpu_physical_cores")
	}
	if err := validateRange("memory_total_bytes", h.MemoryTotalBytes, 0, MaxNodeBytes); err != nil {
		return err
	}
	if err := validateRange("disk_total_bytes", h.DiskTotalBytes, 0, MaxNodeBytes); err != nil {
		return err
	}
	return validateRange("gpu_vram_total_bytes", h.GPUVRAMTotalBytes, 0, MaxNodeBytes)
}

// Validate checks every limit of ADR-0022 Section 9 for the telemetry fields. nil passes.
func (t NodeTelemetry) Validate() error {
	if err := validateRange("cpu_load_1m_milli", t.CPULoad1mMilli, 0, MaxLoadMilli); err != nil {
		return err
	}
	if err := validateRange("memory_available_bytes", t.MemoryAvailableBytes, 0, MaxNodeBytes); err != nil {
		return err
	}
	return validateRange("disk_free_bytes", t.DiskFreeBytes, 0, MaxNodeBytes)
}

// Validate checks the whole registration report.
func (r NodeRegistration) Validate() error {
	if err := ValidateNodeID(r.NodeID); err != nil {
		return err
	}
	if err := ValidateDisplayName(r.DisplayName); err != nil {
		return err
	}
	if err := ValidateContractVersion(r.ContractVersion); err != nil {
		return err
	}
	if err := ValidateAgentVersion(r.AgentVersion); err != nil {
		return err
	}
	return r.Hardware.Validate()
}

// Validate checks the whole telemetry report. The rule that a sample age must not put
// sampled_at before the node's created_at needs the stored node and is the handler's job.
func (r NodeTelemetryReport) Validate() error {
	if err := ValidateContractVersion(r.ContractVersion); err != nil {
		return err
	}
	if err := ValidateSessionID(r.SessionID); err != nil {
		return err
	}
	if r.Seq < MinTelemetrySeq || r.Seq > MaxTelemetrySeq {
		return invalidNode("seq must be %d..%d", MinTelemetrySeq, MaxTelemetrySeq)
	}
	if r.SampleAgeMS < 0 || r.SampleAgeMS > MaxSampleAgeMS {
		return invalidNode("sample_age_ms must be 0..%d", MaxSampleAgeMS)
	}
	return r.NodeTelemetry.Validate()
}

// hardwareHashDomain is the versioned domain separator that starts every hardware content hash
// (ADR-0022 Section 3.2, task V2-1b clarification K1). A change of the encoding needs a new
// version string, which changes every hash.
const hardwareHashDomain = "minicloud.node.hardware.v1"

// ContentHash returns the SHA-256 (lowercase hex, 64 characters) of a canonical encoding of the
// hardware fields only: os, arch, cpu_model, cpu_logical_cores, cpu_physical_cores,
// memory_total_bytes, disk_total_bytes, gpu_count, gpu_model, gpu_vram_total_bytes, in that fixed
// order. agent_version and contract_version are not hashed, so a new agent build on the same
// machine is not a hardware change.
//
// Encoding: the ASCII bytes of hardwareHashDomain and one zero byte, then each field as a one-byte
// marker (0 absent, 1 present) followed, when present, by the value: an integer as 8 bytes big
// endian two's complement, a string as a 4 byte big endian length and then its bytes. The length
// prefix and the markers make the encoding unambiguous: nil and 0 differ, an absent string and an
// empty string differ, and moving bytes between two adjacent strings changes the hash.
func (h NodeHardware) ContentHash() string {
	return h.contentHashWithDomain(hardwareHashDomain)
}

func (h NodeHardware) contentHashWithDomain(domainSep string) string {
	buf := make([]byte, 0, 256)
	buf = append(buf, domainSep...)
	buf = append(buf, 0)
	buf = appendHashString(buf, &h.OS)
	buf = appendHashString(buf, &h.Arch)
	buf = appendHashString(buf, h.CPUModel)
	buf = appendHashInt(buf, h.CPULogicalCores)
	buf = appendHashInt(buf, h.CPUPhysicalCores)
	buf = appendHashInt(buf, h.MemoryTotalBytes)
	buf = appendHashInt(buf, h.DiskTotalBytes)
	buf = appendHashInt(buf, h.GPUCount)
	buf = appendHashString(buf, h.GPUModel)
	buf = appendHashInt(buf, h.GPUVRAMTotalBytes)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:])
}

func appendHashInt(buf []byte, v *int64) []byte {
	if v == nil {
		return append(buf, 0)
	}
	buf = append(buf, 1)
	return binary.BigEndian.AppendUint64(buf, uint64(*v))
}

func appendHashString(buf []byte, v *string) []byte {
	if v == nil {
		return append(buf, 0)
	}
	buf = append(buf, 1)
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(*v)))
	return append(buf, *v...)
}

// HardwareUpdateLimit is the one decision of the hardware update rate limit (ADR-0022 Section 4.1,
// task V2-1b clarification K3), used by both repositories. reportedAt is the newest hardware row's
// reported_at and now is the repository clock (database time in PostgreSQL).
//
// elapsed = now - reportedAt. elapsed < minInterval is limited; elapsed == minInterval and
// elapsed > minInterval are accepted. When limited, retryAfter is (minInterval - elapsed) rounded
// UP to whole seconds and never below one second. A clock that moved backwards (elapsed < 0) is
// limited with retryAfter = minInterval rounded up.
func HardwareUpdateLimit(reportedAt, now time.Time, minInterval time.Duration) (retryAfter time.Duration, limited bool) {
	elapsed := now.Sub(reportedAt)
	if elapsed >= minInterval && elapsed >= 0 {
		return 0, false
	}
	remaining := minInterval - elapsed
	if elapsed < 0 {
		remaining = minInterval
	}
	retryAfter = ceilSeconds(remaining)
	if retryAfter < time.Second {
		retryAfter = time.Second
	}
	return retryAfter, true
}

// ceilSeconds rounds a non-negative duration up to a whole number of seconds.
func ceilSeconds(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return ((d + time.Second - 1) / time.Second) * time.Second
}

// NodeRegistrationResult is what a node registration returns (ADR-0022 Section 4.1): the
// hardware version that is current after the call.
type NodeRegistrationResult struct {
	HardwareVersion int
}

// NodeHardwareView is the current hardware row of a node as a read model. It carries no
// content hash: the hash is coordinator-internal.
type NodeHardwareView struct {
	Version         int       `json:"version"`
	ContractVersion int       `json:"contract_version"`
	AgentVersion    string    `json:"agent_version"`
	ReportedAt      time.Time `json:"reported_at"`
	NodeHardware
}

// NodeTelemetryView is the latest telemetry sample of a node as a read model. It does not carry
// session_id and seq: those are write-path ordering state, not observations.
type NodeTelemetryView struct {
	ContractVersion int       `json:"contract_version"`
	ReceivedAt      time.Time `json:"received_at"`
	SampledAt       time.Time `json:"sampled_at"`
	NodeTelemetry
}

// NodeView is the read model of one node: identity, the current hardware (nil when the node has
// none), the latest telemetry (nil when none) and the IDs of the workers that name the node.
// Unknown values stay nil.
type NodeView struct {
	ID          string             `json:"node_id"`
	DisplayName string             `json:"display_name"`
	CreatedAt   time.Time          `json:"created_at"`
	LastSeenAt  time.Time          `json:"last_seen_at"`
	Hardware    *NodeHardwareView  `json:"hardware"`
	Telemetry   *NodeTelemetryView `json:"telemetry"`
	WorkerIDs   []string           `json:"worker_ids"`
}
