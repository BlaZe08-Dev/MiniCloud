// Package logsafe turns an error into a log attribute that names only its class (SEC-066, P4-22).
//
// A storage, pool or network error can carry the database host and port, user and database name
// (pgconn.ConnectError renders "failed to connect to `user=... database=...`: host:port ...") or
// server message text. Such an error must never reach a log line as text. Class returns a fixed
// label chosen from a closed set and never any part of err.Error(); Err wraps it as the "err"
// attribute. The error value itself is never changed: callers keep classifying, mapping and
// retrying on the original error.
//
// The package uses the standard library and internal/domain only, so packages that must not
// depend on pgx (internal/coordinator) can use it.
package logsafe

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// ClassUnclassified is the label of an error that matches no known class.
const ClassUnclassified = "unclassified"

var domainClasses = []struct {
	err   error
	class string
}{
	{domain.ErrJobNotFound, "domain_job_not_found"},
	{domain.ErrAttemptNotFound, "domain_attempt_not_found"},
	{domain.ErrWorkerNotFound, "domain_worker_not_found"},
	{domain.ErrWorkerMismatch, "domain_worker_mismatch"},
	{domain.ErrStaleAttempt, "domain_stale_attempt"},
	{domain.ErrUnknownTaskType, "domain_unknown_task_type"},
	{domain.ErrPayloadTooLarge, "domain_payload_too_large"},
	{domain.ErrOutputTooLarge, "domain_output_too_large"},
	{domain.ErrTaskTimeout, "domain_task_timeout"},
	{domain.ErrNoWorkersAvailable, "domain_no_workers_available"},
	{domain.ErrWorkerUnhealthy, "domain_worker_unhealthy"},
	{domain.ErrNoQueuedJobs, "domain_no_queued_jobs"},
	{domain.ErrInvalidRequest, "domain_invalid_request"},
	{domain.ErrJobAlreadyTerminal, "domain_job_already_terminal"},
	{domain.ErrJobNotQueued, "domain_job_not_queued"},
	{domain.ErrWorkerTaskUnsupported, "domain_worker_task_unsupported"},
	{domain.ErrWorkerNoCapacity, "domain_worker_no_capacity"},
	{domain.ErrLeaseExpired, "domain_lease_expired"},
}

// Err returns the log attribute for err: key "err", value its class label (see Class).
func Err(err error) slog.Attr {
	return slog.String("err", Class(err))
}

// Class returns a fixed label for err. The label is one of a closed set, or "sqlstate_" followed
// by a five-character SQLSTATE made only of digits and upper-case letters; it never contains any
// other text of err. nil gives "none".
func Class(err error) string {
	if err == nil {
		return "none"
	}
	// A server-reported error (pgconn.PgError implements SQLState) carries only its code here;
	// its message and detail can hold data values. A connect error that wraps a server error
	// (for example 28P01 or 57P03 during startup) is classified by that code.
	var sqlState interface{ SQLState() string }
	if errors.As(err, &sqlState) {
		return sqlStateClass(sqlState.SQLState())
	}
	for _, d := range domainClasses {
		if errors.Is(err, d.err) {
			return d.class
		}
	}
	var transition *domain.InvalidStateTransitionError
	if errors.As(err, &transition) {
		return "domain_invalid_state_transition"
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, syscall.ECONNABORTED):
		return "connection_reset"
	case errors.Is(err, syscall.EPIPE):
		return "broken_pipe"
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTDOWN), errors.Is(err, syscall.ENETDOWN):
		return "host_unreachable"
	case errors.Is(err, syscall.ETIMEDOUT):
		return "timeout"
	}
	if isTLS(err) {
		return "tls"
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network"
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return "connection_closed"
	}
	// Errors built with errors.New inside pgx and puddle (no exported sentinel this module can
	// import without a new direct dependency). The text only selects a label; it is never logged.
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "closed pool"):
		return "closed_pool"
	case strings.Contains(msg, "tls"):
		return "tls"
	case strings.Contains(msg, "timeout"):
		return "timeout"
	case strings.Contains(msg, "cannot parse"):
		return "invalid_database_config"
	case strings.Contains(msg, "failed to connect"), strings.Contains(msg, "conn closed"), strings.Contains(msg, "conn busy"):
		return "connection_failed"
	}
	return ClassUnclassified
}

func isTLS(err error) bool {
	var rh tls.RecordHeaderError
	var rhp *tls.RecordHeaderError
	var alert tls.AlertError
	var verify *tls.CertificateVerificationError
	var unknownAuth x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &rh) || errors.As(err, &rhp) || errors.As(err, &alert) || errors.As(err, &verify) ||
		errors.As(err, &unknownAuth) || errors.As(err, &hostname) || errors.As(err, &invalid)
}

// sqlStateClass returns "sqlstate_<code>" when code is a well-formed SQLSTATE (five digits or
// upper-case letters) and "sqlstate_invalid" otherwise, so a hostile code cannot inject text.
func sqlStateClass(code string) string {
	if len(code) != 5 {
		return "sqlstate_invalid"
	}
	for _, r := range code {
		if !(r >= '0' && r <= '9') && !(r >= 'A' && r <= 'Z') {
			return "sqlstate_invalid"
		}
	}
	return "sqlstate_" + code
}
