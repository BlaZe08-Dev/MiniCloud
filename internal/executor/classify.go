package executor

import (
	"context"
	"errors"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// Classify maps the error of a failed Execute call to the failure class a worker reports with its
// result (ADR-0015, docs/protocol.md section 6):
//
//   - PERMANENT: validation-style failures of an allowlisted task (invalid or oversized payload,
//     unknown task type). The same payload fails the same way, so the job must not be retried.
//   - TRANSIENT: the task hit its execution deadline.
//   - UNKNOWN: everything else, including a cooperative cancellation; retryable by default.
//
// Success (a nil error) has no class. LEASE_EXPIRED is the coordinator's alone and is never
// produced here.
func Classify(err error) domain.ErrorClass {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, domain.ErrInvalidRequest),
		errors.Is(err, domain.ErrUnknownTaskType),
		errors.Is(err, domain.ErrPayloadTooLarge):
		return domain.ClassPermanent
	case errors.Is(err, domain.ErrTaskTimeout), errors.Is(err, context.DeadlineExceeded):
		return domain.ClassTransient
	default:
		return domain.ClassUnknown
	}
}
