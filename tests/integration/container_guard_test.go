package integration_test

import (
	"os"
	"testing"
)

const (
	// envAllowContainerRestart is the first of two opt-in variables.
	envAllowContainerRestart = "MINICLOUD_ALLOW_CONTAINER_RESTART"
	// envConfirmContainer must name the container that will be stopped and started,
	// so a stray export of the first variable alone cannot enable the outage tests.
	envConfirmContainer = "MINICLOUD_CONFIRM_CONTAINER"
	// sharedContainerName is the shared Postgres container that outage tests restart.
	sharedContainerName = "minicloud-postgres"
)

// containerControlAllowed reports whether tests may stop and start the shared Postgres
// container. It is pure (the environment lookup is injected) so it can be unit tested
// without touching Docker. The second result is the reason when control is refused.
func containerControlAllowed(getenv func(string) string) (bool, string) {
	if getenv(envAllowContainerRestart) != "1" {
		return false, envAllowContainerRestart + "=1 is not set"
	}
	if got := getenv(envConfirmContainer); got != sharedContainerName {
		return false, envConfirmContainer + " must equal " + sharedContainerName
	}
	return true, ""
}

// requireContainerControl skips the calling test unless container control is opted in.
// Every test that stops, starts or restarts the shared container must call it first,
// before any other setup or cleanup that touches the container.
func requireContainerControl(t *testing.T) {
	t.Helper()
	if ok, why := containerControlAllowed(os.Getenv); !ok {
		t.Skipf("skipping: this test stops and starts the shared %s container (%s); use `make test-outage`", sharedContainerName, why)
	}
}

// refuseWithoutContainerControl is the defence in depth inside the helpers that run
// docker: if a caller forgot requireContainerControl, fail before any docker call.
func refuseWithoutContainerControl(t *testing.T) {
	t.Helper()
	if ok, why := containerControlAllowed(os.Getenv); !ok {
		t.Fatalf("refusing to control the %s container: %s", sharedContainerName, why)
	}
}
