//go:build !linux

package platform

// NewProbe returns the probe of this platform. V2-1 implements and promises only Linux
// (OD-08); on every other platform the fallback reports os and arch and nothing else.
// stateDir is unused here. A Windows or macOS adapter would add its own file, narrow this
// file's constraint and add its own tests (ADR-0022 Section 6.2); it does not change the
// contract.
func NewProbe(stateDir string) Probe { return newFallbackProbe() }
