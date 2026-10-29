package main

import (
	"fmt"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
)

// failureSummary describes why a job failed, is being retried or was cancelled, using only fields
// the API already serves: the job (state, max_attempts, not_before, result) and its attempts
// (state, error_class, error). A job failed or cancelled by the lease sweeper has no results row,
// so the reason comes from the last attempt. It returns nil for jobs without a failure story.
func failureSummary(job *domain.Job, attempts []*domain.Attempt) []string {
	var last *domain.Attempt
	for _, a := range attempts {
		if last == nil || a.AttemptNo > last.AttemptNo {
			last = a
		}
	}
	used := fmt.Sprintf("%d of %d attempts used", len(attempts), job.MaxAttempts)

	reason := ""
	if last != nil {
		switch {
		case last.ErrorClass != "" && last.Error != "":
			reason = fmt.Sprintf("%s (%s)", last.ErrorClass, sanitize(last.Error))
		case last.ErrorClass != "":
			reason = string(last.ErrorClass)
		case last.Error != "":
			reason = sanitize(last.Error)
		}
	}

	var lines []string
	switch job.State {
	case domain.StateFailed:
		lines = append(lines, fmt.Sprintf("FAILED: %s", used))
		if last != nil && last.ErrorClass == domain.ClassPermanent {
			lines = append(lines, "Note:    permanent failure, not retried")
		}
	case domain.StateRetrying:
		lines = append(lines, fmt.Sprintf("RETRYING: %s", used))
		if job.NotBefore != nil {
			lines = append(lines, fmt.Sprintf("Next retry not before: %s", job.NotBefore.UTC().Format("15:04:05")))
		}
	case domain.StateCancelled:
		lines = append(lines, fmt.Sprintf("CANCELLED: cancel was requested; %s", used))
		if last != nil && last.State == domain.AttemptCancelled && reason == "" {
			lines = append(lines, "Note:    the worker did not report before its lease expired")
		}
	default:
		return nil
	}
	if reason != "" {
		lines = append(lines, "Reason:  "+reason)
	}
	return lines
}
