package faultinject_test

import (
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// TestFault_CoordinatorShutdown_HalfOpenDatabase_Bounded sends SIGTERM to the real coordinator
// while its database is half-open (through the fault proxy) with a request hung in flight, and
// measures how long graceful shutdown takes. The bound asserted is generous (60 s); the measured
// time is logged and recorded as finding F-4e-4.
func TestFault_CoordinatorShutdown_HalfOpenDatabase_Bounded(t *testing.T) {
	pg := sectest.NewPGSchema(t)
	px := sectest.NewFaultProxy(t, pg.UpstreamAddr())
	addr := sectest.FreePort(t)
	bin := sectest.Binaries(t)
	c := sectest.StartProc(t, "coordinator", filepath.Join(bin, "coordinator"), sectest.CoordinatorEnv(addr, pg.URLVia(px.Addr()), realLease, realGrace))
	if !sectest.WaitFor(15*time.Second, func() bool { return sectest.DoRaw("http://"+addr+"/readyz") == http.StatusOK }) {
		t.Fatalf("coordinator not ready:\n%s", redact(c.Out.String(), pg.SchemaURL))
	}
	px.SetMode(sectest.ModeBlackhole)
	hung := make(chan int, 1)
	go func() { hung <- sectest.DoRaw("http://" + addr + "/readyz") }()
	// The request must be inside its database call when the signal arrives; 300 ms is ample for a
	// local request to reach it, and the measurement below does not depend on the exact value.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	c.Term()
	if !c.WaitExit(60 * time.Second) {
		t.Fatalf("coordinator still running 60 s after SIGTERM with a half-open database; output:\n%s", redact(c.Out.String(), pg.SchemaURL))
	}
	t.Logf("graceful shutdown with a half-open database and one hung request took %v", time.Since(start).Round(100*time.Millisecond))
	<-hung
}
