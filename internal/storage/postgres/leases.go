package postgres

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgLockNotAvailable is SQLSTATE 55P03, raised when lock_timeout expires while waiting for a row lock.
const pgLockNotAvailable = "55P03"

// RenewLease extends the lease of a RUNNING attempt to database now plus the lease duration, in
// one transaction that locks the job row and then the attempt row (canonical order jobs ->
// attempts; the workers row is not needed).
//
// Evaluation order (ADR-0014, protocol 6.3), mirroring CommitAttempt:
//  1. Job exists (ErrJobNotFound)
//  2. Job is not terminal (ErrJobAlreadyTerminal)
//  3. An attempt of this job named by the caller was swept to LOST (ErrLeaseExpired)
//  4. Attempt is the job's current attempt, is RUNNING, the token matches in constant time and the
//     job is RUNNING or CANCEL_REQUESTED (ErrStaleAttempt)
//  5. Reporting worker matches (ErrWorkerMismatch)
//
// A lease that has passed but was not swept is still renewed (accept-until-swept). On a
// CANCEL_REQUESTED job the call reports cancel_requested and returns the current expiry without
// extending it. A renewal records no event; every rejection leaves the database unchanged.
func (r *Repository) RenewLease(ctx context.Context, jobID, attemptID, leaseToken, workerID string) (*domain.LeaseRenewal, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	var (
		jobStateStr      string
		currentAttemptID *string
	)
	err = tx.QueryRow(ctx, `SELECT state, current_attempt_id FROM jobs WHERE id = $1 FOR UPDATE;`, jobID).Scan(&jobStateStr, &currentAttemptID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrJobNotFound
		}
		return nil, mapPgError(err)
	}
	jobState := domain.JobState(jobStateStr)
	if jobState.IsTerminal() {
		return nil, domain.ErrJobAlreadyTerminal
	}

	var (
		attState       string
		attWorkerID    string
		leaseTokenHash string
		leaseExpiresAt time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT state, worker_id, lease_token_hash, lease_expires_at
		FROM attempts
		WHERE id = $1 AND job_id = $2
		FOR UPDATE;`, attemptID, jobID).Scan(&attState, &attWorkerID, &leaseTokenHash, &leaseExpiresAt)
	found := true
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, mapPgError(err)
		}
		found = false
	}
	if found && attState == string(domain.AttemptLost) {
		return nil, domain.ErrLeaseExpired
	}
	if currentAttemptID == nil || *currentAttemptID != attemptID {
		return nil, domain.ErrStaleAttempt
	}
	if !found {
		return nil, domain.ErrAttemptNotFound
	}
	if attState != string(domain.AttemptRunning) {
		return nil, domain.ErrStaleAttempt
	}
	providedHash := domain.HashLeaseToken(leaseToken)
	if subtle.ConstantTimeCompare([]byte(leaseTokenHash), []byte(providedHash)) != 1 {
		return nil, domain.ErrStaleAttempt
	}
	if attWorkerID != workerID {
		return nil, &domain.WorkerMismatchError{AssignedWorker: attWorkerID, ReportingWorker: workerID}
	}

	switch jobState {
	case domain.StateCancelRequested:
		// Cancellation delivery channel: report it, do not extend the lease.
		return &domain.LeaseRenewal{LeaseExpiresAt: leaseExpiresAt, CancelRequested: true}, nil
	case domain.StateRunning:
		var renewed time.Time
		err = tx.QueryRow(ctx, `
			UPDATE attempts
			SET lease_expires_at = clock_timestamp() + make_interval(secs => $2::double precision)
			WHERE id = $1
			RETURNING lease_expires_at;`, attemptID, r.leaseDuration.Seconds()).Scan(&renewed)
		if err != nil {
			return nil, mapPgError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, mapPgError(err)
		}
		return &domain.LeaseRenewal{LeaseExpiresAt: renewed}, nil
	default:
		return nil, domain.ErrStaleAttempt
	}
}

// sweepDisposition is what happened to one sweep candidate.
type sweepDisposition int

const (
	sweepSwept sweepDisposition = iota
	sweepSkippedLocked
	sweepSkippedChanged
	sweepSuppressed
)

type sweepCandidate struct {
	attemptID string
	jobID     string
	workerID  string
}

// SweepExpiredLeases closes RUNNING attempts whose lease has passed on database time, oldest lease
// first, at most params.Limit per call, and resolves their jobs (see the in-memory repository for
// the rules; the two implementations are held to identical behaviour by the shared conformance
// suite).
//
// Transaction shape (ADR-0014, docs/schema.md section 5): an unlocked candidate scan over
// idx_attempts_lease_expiry, then one transaction per candidate that takes row locks in the
// canonical order
//
//	jobs (FOR UPDATE SKIP LOCKED) -> workers (FOR UPDATE) -> attempts (FOR UPDATE)
//
// with lock_timeout set for the two blocking locks. Inside the locks it re-checks that the attempt
// is still RUNNING and still expired (a renewal or commit may have landed since the scan), then
// applies the caller's grace state: the only authoritative grace check. A SKIP LOCKED miss or a
// lock timeout skips the candidate and is counted, not returned: only a returned error is a
// failed tick.
func (r *Repository) SweepExpiredLeases(ctx context.Context, params domain.SweepParams) (*domain.SweepResult, error) {
	if params.Backoff == nil {
		return nil, domain.ErrInvalidRequest
	}
	limit := params.Limit
	if limit <= 0 {
		limit = domain.DefaultSweepBatchSize
	}

	rows, err := r.pool.Query(ctx, `
		SELECT a.id, a.job_id, a.worker_id
		FROM attempts a
		WHERE a.state = 'RUNNING'
		  AND a.lease_expires_at < clock_timestamp()
		ORDER BY a.lease_expires_at ASC, a.id ASC
		LIMIT $1;`, limit)
	if err != nil {
		return nil, mapPgError(err)
	}
	var candidates []sweepCandidate
	for rows.Next() {
		var c sweepCandidate
		if err := rows.Scan(&c.attemptID, &c.jobID, &c.workerID); err != nil {
			rows.Close()
			return nil, mapPgError(err)
		}
		candidates = append(candidates, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	res := &domain.SweepResult{Candidates: len(candidates)}
	for _, c := range candidates {
		outcome, disp, err := r.sweepCandidate(ctx, c, params)
		if err != nil {
			return res, err
		}
		switch disp {
		case sweepSwept:
			res.Swept = append(res.Swept, *outcome)
		case sweepSkippedLocked:
			res.SkippedLocked++
		case sweepSkippedChanged:
			res.SkippedChanged++
		case sweepSuppressed:
			res.SuppressedByGrace++
		}
	}
	return res, nil
}

func isLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgLockNotAvailable
}

// sweepCandidate resolves one candidate in its own transaction.
func (r *Repository) sweepCandidate(ctx context.Context, c sweepCandidate, params domain.SweepParams) (*domain.SweepOutcome, sweepDisposition, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	// lock_timeout applies to the two blocking locks below; SKIP LOCKED never waits.
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true);`, lockTimeoutSetting(r.sweepLockTimeout)); err != nil {
		return nil, 0, mapPgError(err)
	}

	// 1. jobs, SKIP LOCKED: a job locked by a concurrent commit, renewal, cancel or dispatch is
	// left for the next tick.
	var (
		jobStateStr string
		maxAttempts int
	)
	err = tx.QueryRow(ctx, `SELECT state, max_attempts FROM jobs WHERE id = $1 FOR UPDATE SKIP LOCKED;`, c.jobID).Scan(&jobStateStr, &maxAttempts)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, sweepSkippedLocked, nil
		}
		return nil, 0, mapPgError(err)
	}
	jobState := domain.JobState(jobStateStr)

	// 2. workers, plain FOR UPDATE under lock_timeout.
	var lockedWorker string
	err = tx.QueryRow(ctx, `SELECT id FROM workers WHERE id = $1 FOR UPDATE;`, c.workerID).Scan(&lockedWorker)
	if err != nil {
		if isLockNotAvailable(err) {
			return nil, sweepSkippedLocked, nil
		}
		return nil, 0, mapPgError(err)
	}

	// 3. attempts, plain FOR UPDATE under lock_timeout; database time is read under the lock.
	var (
		attState       string
		attemptNo      int
		leaseExpiresAt time.Time
		now            time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT state, attempt_no, lease_expires_at, clock_timestamp()
		FROM attempts
		WHERE id = $1
		FOR UPDATE;`, c.attemptID).Scan(&attState, &attemptNo, &leaseExpiresAt, &now)
	if err != nil {
		if isLockNotAvailable(err) {
			return nil, sweepSkippedLocked, nil
		}
		return nil, 0, mapPgError(err)
	}

	// Re-check inside the locks: a concurrent renewal or commit may have changed the attempt
	// since the candidate scan.
	if attState != string(domain.AttemptRunning) || !leaseExpiresAt.Before(now) {
		return nil, sweepSkippedChanged, nil
	}
	// The only authoritative grace check.
	if params.Grace.Suppresses(now) {
		return nil, sweepSuppressed, nil
	}

	outcome, err := applySweep(ctx, tx, jobState, maxAttempts, attemptNo, leaseExpiresAt, now, c, params.Backoff)
	if err != nil {
		return nil, 0, err
	}
	if outcome == nil {
		return nil, sweepSkippedChanged, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, mapPgError(err)
	}
	return outcome, sweepSwept, nil
}

// lockTimeoutSetting renders a duration as a PostgreSQL lock_timeout value in milliseconds.
func lockTimeoutSetting(d time.Duration) string {
	ms := d.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	return strconv.FormatInt(ms, 10) + "ms"
}

// applySweep performs the resolution of one locked, expired RUNNING attempt. It returns nil
// without writing when the job is in a state the sweeper has no rule for.
func applySweep(ctx context.Context, tx pgx.Tx, jobState domain.JobState, maxAttempts, attemptNo int, leaseExpiresAt, now time.Time, c sweepCandidate, backoff domain.BackoffFunc) (*domain.SweepOutcome, error) {
	const lostError = "lease expired: worker failed to renew"

	closeAttempt := func(state domain.AttemptState) error {
		var class *string
		var errText *string
		if state == domain.AttemptLost {
			cl, msg := string(domain.ClassLeaseExpired), lostError
			class, errText = &cl, &msg
		}
		_, err := tx.Exec(ctx, `
			UPDATE attempts
			SET state = $1, finished_at = $2, error_class = $3, error = $4
			WHERE id = $5;`, string(state), now, class, errText, c.attemptID)
		return mapPgError(err)
	}
	setJob := func(target domain.JobState, notBefore *time.Time) error {
		if err := domain.ValidateJobTransition(jobState, target); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE jobs SET state = $1, not_before = $2, updated_at = $3 WHERE id = $4;`,
			string(target), notBefore, now, c.jobID)
		return mapPgError(err)
	}
	event := func(typ domain.JobEventType, from, to *domain.JobState, details map[string]any) error {
		raw, err := json.Marshal(details)
		if err != nil {
			return err
		}
		var fromStr, toStr *string
		if from != nil {
			s := string(*from)
			fromStr = &s
		}
		if to != nil {
			s := string(*to)
			toStr = &s
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, details, created_at)
			VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7);`,
			c.jobID, c.attemptID, string(typ), fromStr, toStr, string(raw), now)
		return mapPgError(err)
	}
	leaseExpiredEvent := func() error {
		return event(domain.JobEventLeaseExpired, nil, nil, map[string]any{
			"attempt_id": c.attemptID,
			"worker_id":  c.workerID,
			"expired_at": leaseExpiresAt.UTC().Format(time.RFC3339Nano),
		})
	}

	finalJobState := jobState
	var finalAttemptState domain.AttemptState

	switch {
	case jobState.IsTerminal():
		// The job is never mutated; only the attempt is closed.
		finalAttemptState = domain.AttemptLost
		if jobState == domain.StateCancelled {
			finalAttemptState = domain.AttemptCancelled
		}
		if err := closeAttempt(finalAttemptState); err != nil {
			return nil, err
		}
		if err := leaseExpiredEvent(); err != nil {
			return nil, err
		}

	case jobState == domain.StateCancelRequested:
		// Cancel wins: never RETRYING, and only a JOB_CANCELLED event.
		finalAttemptState, finalJobState = domain.AttemptCancelled, domain.StateCancelled
		if err := closeAttempt(finalAttemptState); err != nil {
			return nil, err
		}
		if err := setJob(finalJobState, nil); err != nil {
			return nil, err
		}
		from, to := jobState, finalJobState
		if err := event(domain.JobEventCancelled, &from, &to, map[string]any{
			"resolved_by": "lease_sweeper",
			"reason":      "worker unresponsive",
		}); err != nil {
			return nil, err
		}

	case jobState == domain.StateRunning && attemptNo < maxAttempts:
		delay := backoff(attemptNo)
		if delay < 0 {
			delay = 0
		}
		notBefore := now.Add(delay)
		finalAttemptState, finalJobState = domain.AttemptLost, domain.StateRetrying
		if err := closeAttempt(finalAttemptState); err != nil {
			return nil, err
		}
		if err := setJob(finalJobState, &notBefore); err != nil {
			return nil, err
		}
		if err := leaseExpiredEvent(); err != nil {
			return nil, err
		}
		from, to := jobState, finalJobState
		if err := event(domain.JobEventRetrying, &from, &to, map[string]any{
			"attempt_no":      attemptNo,
			"not_before":      notBefore.UTC().Format(time.RFC3339Nano),
			"backoff_seconds": delay.Seconds(),
		}); err != nil {
			return nil, err
		}

	case jobState == domain.StateRunning:
		finalAttemptState, finalJobState = domain.AttemptLost, domain.StateFailed
		if err := closeAttempt(finalAttemptState); err != nil {
			return nil, err
		}
		if err := setJob(finalJobState, nil); err != nil {
			return nil, err
		}
		if err := leaseExpiredEvent(); err != nil {
			return nil, err
		}
		from, to := jobState, finalJobState
		if err := event(domain.JobEventFailed, &from, &to, map[string]any{
			"attempt_no":  attemptNo,
			"error":       "max attempts exhausted",
			"error_class": string(domain.ClassLeaseExpired),
		}); err != nil {
			return nil, err
		}

	default:
		return nil, nil
	}

	return &domain.SweepOutcome{JobID: c.jobID, AttemptID: c.attemptID, AttemptState: finalAttemptState, JobState: finalJobState}, nil
}
