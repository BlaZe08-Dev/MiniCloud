package sectest

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ProxyMode is the behaviour of a FaultProxy.
type ProxyMode int32

const (
	// ModeForward relays bytes both ways.
	ModeForward ProxyMode = iota
	// ModeDrop closes every live connection and closes new ones at accept (peer sees EOF / refusal-like).
	ModeDrop
	// ModeReset closes every live connection with SO_LINGER=0 so the peer sees RST, and does the same at accept.
	ModeReset
	// ModeBlackhole accepts and reads but never forwards and never answers (half-open: the peer's
	// writes succeed, its reads block forever).
	ModeBlackhole
	// ModeDelay relays bytes after waiting Delay in each direction.
	ModeDelay
)

func (m ProxyMode) String() string {
	return [...]string{"forward", "drop", "reset", "blackhole", "delay"}[m]
}

// FaultProxy is an in-test TCP proxy placed between a client and one upstream. It is the only way
// the P4-4e suites simulate a database outage or a network partition: the real database container
// and the real processes are never stopped.
type FaultProxy struct {
	t        testing.TB
	ln       net.Listener
	upstream string
	mode     atomic.Int32
	delay    atomic.Int64 // nanoseconds

	mu    sync.Mutex
	conns map[net.Conn]struct{}
	wg    sync.WaitGroup
	done  chan struct{}

	Accepted atomic.Int64
}

// NewFaultProxy starts a proxy to upstream on 127.0.0.1:0 in ModeForward and stops it on cleanup.
func NewFaultProxy(t testing.TB, upstream string) *FaultProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("proxy listen: %v", err)
	}
	p := &FaultProxy{t: t, ln: ln, upstream: upstream, conns: map[net.Conn]struct{}{}, done: make(chan struct{})}
	p.wg.Add(1)
	go p.acceptLoop()
	t.Cleanup(p.Close)
	return p
}

// Addr is the proxy's listen address.
func (p *FaultProxy) Addr() string { return p.ln.Addr().String() }

// SetDelay sets the per-direction latency used in ModeDelay.
func (p *FaultProxy) SetDelay(d time.Duration) { p.delay.Store(int64(d)) }

// SetMode switches the behaviour. Switching to ModeDrop or ModeReset severs live connections at
// once; switching to ModeBlackhole leaves them open and stops relaying so they become half-open.
func (p *FaultProxy) SetMode(m ProxyMode) {
	p.mode.Store(int32(m))
	switch m {
	case ModeDrop:
		p.severAll(false)
	case ModeReset:
		p.severAll(true)
	}
}

// Mode returns the current mode.
func (p *FaultProxy) Mode() ProxyMode { return ProxyMode(p.mode.Load()) }

// Close stops the listener and severs everything.
func (p *FaultProxy) Close() {
	select {
	case <-p.done:
		return
	default:
		close(p.done)
	}
	_ = p.ln.Close()
	p.severAll(false)
	p.wg.Wait()
}

func (p *FaultProxy) track(c net.Conn) {
	p.mu.Lock()
	p.conns[c] = struct{}{}
	p.mu.Unlock()
}

func (p *FaultProxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

func (p *FaultProxy) severAll(reset bool) {
	p.mu.Lock()
	cs := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		cs = append(cs, c)
	}
	p.mu.Unlock()
	for _, c := range cs {
		closeConn(c, reset)
	}
}

func closeConn(c net.Conn, reset bool) {
	if reset {
		if tc, ok := c.(*net.TCPConn); ok {
			_ = tc.SetLinger(0)
		}
	}
	_ = c.Close()
}

func (p *FaultProxy) acceptLoop() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.Accepted.Add(1)
		switch p.Mode() {
		case ModeDrop:
			closeConn(c, false)
			continue
		case ModeReset:
			closeConn(c, true)
			continue
		}
		p.track(c)
		p.wg.Add(1)
		go p.serve(c)
	}
}

func (p *FaultProxy) serve(client net.Conn) {
	defer p.wg.Done()
	defer p.untrack(client)
	defer client.Close()

	// In blackhole mode the upstream is never contacted: the client is half-open from the start.
	// Connections made earlier keep their upstream link but stop relaying when the mode flips.
	var up net.Conn
	if p.Mode() != ModeBlackhole {
		var err error
		up, err = net.DialTimeout("tcp", p.upstream, 5*time.Second)
		if err != nil {
			return
		}
		p.track(up)
		defer p.untrack(up)
		defer up.Close()
	}

	var wg sync.WaitGroup
	pipe := func(dst, src net.Conn) {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				switch p.Mode() {
				case ModeBlackhole:
					// swallow: written bytes are accepted and discarded, nothing is answered
					continue
				case ModeDelay:
					if d := time.Duration(p.delay.Load()); d > 0 {
						// Injected latency is the fault being simulated, not a synchronisation sleep.
						select {
						case <-time.After(d):
						case <-p.done:
							return
						}
					}
				}
				if dst == nil {
					continue
				}
				if _, werr := dst.Write(buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if dst != nil {
					if tc, ok := dst.(*net.TCPConn); ok {
						_ = tc.CloseWrite()
					}
				}
				return
			}
		}
	}
	wg.Add(1)
	go pipe(up, client)
	if up != nil {
		wg.Add(1)
		go pipe(client, up)
	}
	wg.Wait()
}
