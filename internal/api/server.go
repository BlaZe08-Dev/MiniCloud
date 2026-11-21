package api

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/coordinator"
)

const (
	// MaxRequestSizeBytes enforces 1 MiB max body size on incoming HTTP requests.
	MaxRequestSizeBytes = 1024 * 1024

	// RequestTimeout bounds how long one request may wait on the repository. It sits below the
	// server WriteTimeout so a half-open database connection yields a 503 instead of a hung
	// request (F-4e-2). A handler that outlives it sees context.DeadlineExceeded, which
	// mapDomainErrorToHTTP maps to 503 SERVICE_UNAVAILABLE.
	RequestTimeout = 8 * time.Second

	// DefaultReadHeaderTimeout bounds the time allowed to read request headers (mitigates Slowloris, SEC-009).
	DefaultReadHeaderTimeout = 2 * time.Second
)

// Server wraps the standard library net/http Server for the MiniCloud coordinator.
type Server struct {
	httpServer *http.Server
	mux        *http.ServeMux
	handler    *Handler
	// localOnly is the startup decision of ADR-0022 Section 4.4 (config.IsLocalOnlyAddr). It is
	// fixed at construction and never recomputed or read from a request.
	localOnly bool
}

// Option configures NewServer.
type Option func(*Server)

// WithLocalOnly sets the local-only decision. The default is false: the node routes are not
// registered and the V1 shapes are kept.
func WithLocalOnly(localOnly bool) Option {
	return func(s *Server) { s.localOnly = localOnly }
}

// NewServer configures an HTTP server with route pattern matching, limits, and timeouts.
func NewServer(addr string, coord *coordinator.Service, opts ...Option) *Server {
	mux := http.NewServeMux()
	h := NewHandler(coord)

	s := &Server{
		mux:     mux,
		handler: h,
	}
	for _, opt := range opts {
		opt(s)
	}
	h.localOnly = s.localOnly
	s.registerRoutes()

	// Apply global middleware: Panic Recovery and Request Size Limit (1 MiB)
	var handler http.Handler = mux
	handler = maxBytesMiddleware(MaxRequestSizeBytes)(handler)
	handler = requestDeadlineMiddleware(RequestTimeout)(handler)
	handler = recoveryMiddleware(handler)

	s.httpServer = &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: DefaultReadHeaderTimeout,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

func (s *Server) registerRoutes() {
	// Health and Readiness
	s.mux.HandleFunc("GET /healthz", s.handler.HandleHealthz)
	s.mux.HandleFunc("GET /readyz", s.handler.HandleReadyz)

	// Public Client Job API
	s.mux.HandleFunc("POST /v1/jobs", s.handler.HandleSubmitJob)
	s.mux.HandleFunc("GET /v1/jobs/{id}", s.handler.HandleGetJob)
	s.mux.HandleFunc("GET /v1/jobs/{id}/attempts", s.handler.HandleListAttempts)
	s.mux.HandleFunc("POST /v1/jobs/{id}/cancel", s.handler.HandleCancelJob)
	s.mux.HandleFunc("GET /v1/workers", s.handler.HandleListWorkers)

	// Worker Coordination Protocol
	s.mux.HandleFunc("POST /v1/workers/register", s.handler.HandleRegisterWorker)
	s.mux.HandleFunc("POST /v1/workers/{id}/heartbeat", s.handler.HandleWorkerHeartbeat)
	s.mux.HandleFunc("POST /v1/workers/{id}/poll", s.handler.HandlePollWork)
	s.mux.HandleFunc("POST /v1/jobs/{id}/attempts/{attempt_id}/result", s.handler.HandleReportResult)
	s.mux.HandleFunc("POST /v1/jobs/{id}/attempts/{attempt_id}/renew", s.handler.HandleRenewLease)

	// Node routes (ADR-0022 Section 4.4): registered only when local-only; otherwise the router's
	// normal 404 for an unknown route applies.
	if s.localOnly {
		s.mux.HandleFunc("POST /v1/nodes/register", s.handler.HandleRegisterNode)
		s.mux.HandleFunc("POST /v1/nodes/{node_id}/telemetry", s.handler.HandleNodeTelemetry)
		s.mux.HandleFunc("GET /v1/nodes", s.handler.HandleListNodes)
		s.mux.HandleFunc("GET /v1/nodes/{node_id}", s.handler.HandleGetNode)
	}
}

// Handler returns the underlying http.Handler (useful for httptest.Server in tests).
func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

// Start listens and serves on the configured address.
func (s *Server) Start() error {
	return s.httpServer.ListenAndServe()
}

// Serve accepts incoming connections on the listener ln.
func (s *Server) Serve(ln net.Listener) error {
	return s.httpServer.Serve(ln)
}

// Shutdown gracefully stops the HTTP server without interrupting in-flight requests.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}
