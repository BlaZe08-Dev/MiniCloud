package faultinject_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/tests/internal/sectest"
)

// echoServer accepts connections and echoes bytes back.
func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return ln.Addr().String()
}

func roundTrip(c net.Conn, deadline time.Duration) error {
	_ = c.SetDeadline(time.Now().Add(deadline))
	if _, err := c.Write([]byte("ping")); err != nil {
		return err
	}
	buf := make([]byte, 4)
	_, err := io.ReadFull(c, buf)
	return err
}

// TestFault_ProxySelfTest proves each proxy mode does what the outage tests rely on, so a green
// outage test cannot be an artefact of a proxy that never faulted.
func TestFault_ProxySelfTest(t *testing.T) {
	up := echoServer(t)
	p := sectest.NewFaultProxy(t, up)

	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := roundTrip(c, 2*time.Second); err != nil {
		t.Fatalf("forward: %v", err)
	}

	p.SetMode(sectest.ModeBlackhole)
	if err := roundTrip(c, 300*time.Millisecond); err == nil {
		t.Fatal("blackhole: round trip succeeded") // 300ms bound: the expected outcome is a read timeout
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("blackhole: want read timeout (half-open), got %v", err)
	}

	p.SetMode(sectest.ModeDrop)
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("drop: live connection still readable")
	}
	c2, err := net.Dial("tcp", p.Addr())
	if err == nil {
		if rerr := roundTrip(c2, 2*time.Second); rerr == nil {
			t.Fatal("drop: new connection served")
		}
		c2.Close()
	}

	p.SetMode(sectest.ModeForward)
	c3, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c3.Close()
	p.SetMode(sectest.ModeReset)
	_ = c3.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := c3.Read(make([]byte, 1)); err == nil {
		t.Fatal("reset: connection survived")
	}

	p.SetMode(sectest.ModeDelay)
	p.SetDelay(150 * time.Millisecond)
	c4, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c4.Close()
	start := time.Now()
	if err := roundTrip(c4, 5*time.Second); err != nil {
		t.Fatalf("delay: %v", err)
	}
	if el := time.Since(start); el < 250*time.Millisecond {
		t.Fatalf("delay: round trip took %v, want >= 2x150ms", el)
	}
}
