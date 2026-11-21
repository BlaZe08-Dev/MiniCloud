package platform

import (
	"context"
	"runtime"
	"testing"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// TestProbeFallback_OnlyOSArch builds the non-Linux fallback on any platform through the
// internal constructor: it fills os and arch and leaves every other field nil.
func TestProbeFallback_OnlyOSArch(t *testing.T) {
	p := newFallbackProbe()

	h, err := p.Hardware(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := domain.NodeHardware{OS: runtime.GOOS, Arch: runtime.GOARCH}
	if h != want {
		t.Fatalf("hardware = %+v, want only os and arch %+v", h, want)
	}
	if h.CPUModel != nil || h.CPULogicalCores != nil || h.CPUPhysicalCores != nil ||
		h.MemoryTotalBytes != nil || h.DiskTotalBytes != nil ||
		h.GPUCount != nil || h.GPUModel != nil || h.GPUVRAMTotalBytes != nil {
		t.Fatalf("fallback must leave every other field nil: %+v", h)
	}
	if err := h.Validate(); err != nil {
		t.Fatalf("fallback hardware must validate: %v", err)
	}

	tel, err := p.Telemetry(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tel != (domain.NodeTelemetry{}) {
		t.Fatalf("fallback telemetry must be all nil: %+v", tel)
	}
}

func TestProbeFallback_HonoursCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := newFallbackProbe()
	if _, err := p.Hardware(ctx); err == nil {
		t.Fatal("Hardware: want context error")
	}
	if _, err := p.Telemetry(ctx); err == nil {
		t.Fatal("Telemetry: want context error")
	}
}
