// Package platform is the OS adapter boundary of ADR-0022 Section 6.2. It imports only
// internal/domain among project packages, and no other project package imports it except
// the worker (later slices). The capability contract is OS independent; everything that
// reads the operating system lives here.
package platform

import (
	"context"
	"runtime"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Probe reports what this machine can tell; a field it cannot measure is nil (unknown).
// 0 is a real measured value and is never used for "unknown" (ADR-0022 Section 10).
type Probe interface {
	Hardware(ctx context.Context) (domain.NodeHardware, error)
	Telemetry(ctx context.Context) (domain.NodeTelemetry, error)
}

// fallbackProbe is the probe for platforms without an adapter (ADR-0022 Section 6.2, OD-08):
// it fills only os and arch from runtime and leaves every other field nil. It is defined
// here without a build tag so a test can build it on Linux through newFallbackProbe;
// probe_other.go only selects it for non-Linux builds.
type fallbackProbe struct{}

func newFallbackProbe() Probe { return fallbackProbe{} }

func (fallbackProbe) Hardware(ctx context.Context) (domain.NodeHardware, error) {
	if err := ctx.Err(); err != nil {
		return domain.NodeHardware{}, err
	}
	return domain.NodeHardware{OS: runtime.GOOS, Arch: runtime.GOARCH}, nil
}

func (fallbackProbe) Telemetry(ctx context.Context) (domain.NodeTelemetry, error) {
	if err := ctx.Err(); err != nil {
		return domain.NodeTelemetry{}, err
	}
	return domain.NodeTelemetry{}, nil
}
