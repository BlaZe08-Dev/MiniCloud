package platform

import (
	"fmt"
	"os"
	"testing"
	"time"
)

// Helper-process mode for TestNodeIDFile_HelperProcesses_RaceOneDirectory: the test re-executes
// this test binary, and TestMain runs the child side before any test. Using TestMain means no
// placeholder test exists that "passes" when it is not a child.
const (
	helperDirEnv  = "MC_NODEID_HELPER_DIR"
	helperGateEnv = "MC_NODEID_HELPER_GATE"
)

func TestMain(m *testing.M) {
	if dir := os.Getenv(helperDirEnv); dir != "" {
		os.Exit(runNodeIDHelper(dir, os.Getenv(helperGateEnv)))
	}
	os.Exit(m.Run())
}

// runNodeIDHelper waits for the gate file (so all children start together), loads or creates
// the node ID, and prints it.
func runNodeIDHelper(dir, gate string) int {
	deadline := time.Now().Add(20 * time.Second)
	for gate != "" {
		if _, err := os.Stat(gate); err == nil {
			break
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "helper: gate file never appeared")
			return 3
		}
		time.Sleep(time.Millisecond)
	}
	id, err := LoadOrCreateNodeID(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper:", err)
		return 1
	}
	fmt.Println(id)
	return 0
}
