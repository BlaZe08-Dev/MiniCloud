package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/BlaZe08-Dev/Minicloud/internal/domain"
	"github.com/BlaZe08-Dev/Minicloud/internal/scheduler"
	"github.com/BlaZe08-Dev/Minicloud/internal/storage"
	"github.com/jackc/pgx/v5"
)

// The PostgreSQL side of ADR-0017 (assignment-based round-robin, slice 5b). Every staleness and
// expiry comparison runs in SQL on the database clock (clock_timestamp(), the time at the
// statement), and the pass heartbeat is written with clock_timestamp() by the pass's last statement,
// so last_completed_at is the completion time of the pass. Go passes durations, never a wall-clock
// time, into those comparisons (implementation note 8).
//
// Lock order is the repository order, jobs -> workers -> attempts -> results -> job_events:
//   - the pass takes pg_try_advisory_xact_lock first (never waits), then jobs FOR UPDATE SKIP
//     LOCKED, then one worker row FOR UPDATE under lock_timeout per reservation, then reads
//     attempts without a lock, then writes jobs and job_events, then the cursors and the heartbeat;
//   - the claim takes jobs FOR UPDATE SKIP LOCKED, then the worker row FOR UPDATE, then inserts the
//     attempt and the event, exactly as DispatchNextJob.

var (
	_ storage.AssignmentRepository    = (*Repository)(nil)
	_ storage.AssignmentPassStaleness = (*Repository)(nil)
)

// AssignmentPassLockID is the fixed key of the assignment pass advisory lock (ADR-0017 Sections
// 2.1 and 4.2). It differs from the migrator's DefaultAdvisoryLockID, so a pass never waits on a
// migration and a migration never waits on a pass. Derived from "MCAssgn".
const AssignmentPassLockID int64 = 0x4d43417373676e

const (
	// assignmentPollWriteThrottleSeconds is the poll freshness throttle of ADR-0017 Section 2.2:
	// last_poll_at is written only when it is older than one second.
	assignmentPollWriteThrottleSeconds = 1.0

	// assignmentEventCandidateCap caps the candidate IDs in a JOB_ASSIGNED payload (ADR-0017
	// Section 2.1, INV-18). It is a constant, not configuration.
	assignmentEventCandidateCap = 10
)

// Every query below spells out the dispatch gate of DispatchNextJob (ADR-0017 C1, C20) on database
// time: state QUEUED, or RETRYING with not_before reached. The queries are complete literals,
// because TestPostgresRepository_Security_ASTCheck_NoDynamicSQLConcatenation forbids building SQL
// with string concatenation.

// touchPollSQL writes a HEALTHY worker's poll freshness, throttled (ADR-0017 Section 2.2).
const touchPollSQL = `
	UPDATE workers
	SET last_poll_at = clock_timestamp()
	WHERE id = $1
	  AND status = 'HEALTHY'
	  AND (last_poll_at IS NULL OR last_poll_at < clock_timestamp() - make_interval(secs => $2::double precision));`

// RecordPoll writes the poll freshness of a HEALTHY worker in its own statement, which holds no
// other lock (ADR-0017 Sections 2.2 and 4.3). An unknown worker is ErrWorkerNotFound and an
// UNHEALTHY one ErrWorkerUnhealthy; neither is written.
func (r *Repository) RecordPoll(ctx context.Context, workerID string) error {
	tag, err := r.pool.Exec(ctx, touchPollSQL, workerID, assignmentPollWriteThrottleSeconds)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var status string
	if err := r.pool.QueryRow(ctx, `SELECT status FROM workers WHERE id = $1;`, workerID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrWorkerNotFound
		}
		return mapPgError(err)
	}
	if status != string(domain.WorkerStatusHealthy) {
		return domain.ErrWorkerUnhealthy
	}
	return nil // throttled
}

// AssignmentPassState returns last_completed_at of the pass heartbeat row. Migration 000005
// creates the row with the migration time, so PostgreSQL never returns the zero time.
func (r *Repository) AssignmentPassState(ctx context.Context) (time.Time, error) {
	var at time.Time
	if err := r.pool.QueryRow(ctx, `SELECT last_completed_at FROM assignment_pass_state WHERE singleton;`).Scan(&at); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, mapPgError(err)
	}
	return at.UTC(), nil
}

// AssignmentPassStale compares the pass heartbeat with the database clock (implementation note 8).
// A missing heartbeat row counts as stale.
func (r *Repository) AssignmentPassStale(ctx context.Context, threshold time.Duration) (bool, error) {
	var stale bool
	err := r.pool.QueryRow(ctx, `
		SELECT COALESCE(
			(SELECT last_completed_at < clock_timestamp() - make_interval(secs => $1::double precision)
			 FROM assignment_pass_state WHERE singleton),
			TRUE);`, threshold.Seconds()).Scan(&stale)
	if err != nil {
		return false, mapPgError(err)
	}
	return stale, nil
}

type revokeRow struct {
	jobID     string
	workerID  string
	unhealthy bool
}

// AssignQueuedJobs runs one assignment pass in one transaction (ADR-0017 Section 2.1): the
// advisory try-lock, then revoke, then reserve per task type, then the cursors and the heartbeat.
// A pass that cannot take the advisory lock returns Skipped and has read and written no row.
func (r *Repository) AssignQueuedJobs(ctx context.Context, p storage.AssignParams) (storage.AssignResult, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return storage.AssignResult{}, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	// N3: the first statement. It never blocks; false means another pass holds the lock.
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1);`, AssignmentPassLockID).Scan(&locked); err != nil {
		return storage.AssignResult{}, mapPgError(err)
	}
	if !locked {
		return storage.AssignResult{Skipped: true}, nil
	}
	// lock_timeout bounds the worker row lock of each reservation (Section 2.4). SKIP LOCKED on
	// jobs never waits.
	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true);`, lockTimeoutSetting(r.sweepLockTimeout)); err != nil {
		return storage.AssignResult{}, mapPgError(err)
	}

	res := storage.AssignResult{CandidatesRead: make(map[string]int)}
	if err := assignRevoke(ctx, tx, p.BatchLimit, &res); err != nil {
		return storage.AssignResult{}, err
	}
	cursors, err := assignReserve(ctx, tx, p, &res)
	if err != nil {
		return storage.AssignResult{}, err
	}

	for taskType, workerID := range cursors {
		if _, err := tx.Exec(ctx, `
			INSERT INTO assignment_cursor (task_type, last_worker_id, updated_at)
			VALUES ($1, $2, clock_timestamp())
			ON CONFLICT (task_type) DO UPDATE
			SET last_worker_id = EXCLUDED.last_worker_id, updated_at = EXCLUDED.updated_at;`, taskType, workerID); err != nil {
			return storage.AssignResult{}, mapPgError(err)
		}
	}
	// The pass heartbeat, as the last statement, on the database clock at that statement
	// (clock_timestamp(), not now(), which is the transaction start): last_completed_at is the
	// completion time of the pass (note 8, fix round 1).
	if _, err := tx.Exec(ctx, `
		INSERT INTO assignment_pass_state (singleton, last_completed_at) VALUES (TRUE, clock_timestamp())
		ON CONFLICT (singleton) DO UPDATE SET last_completed_at = EXCLUDED.last_completed_at;`); err != nil {
		return storage.AssignResult{}, mapPgError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storage.AssignResult{}, mapPgError(err)
	}
	return res, nil
}

// assignRevoke clears at most batchLimit reservations whose worker is no longer HEALTHY (revoked,
// no lapse counted) or whose window has expired (lapsed, counted), oldest job first (ADR-0017
// Sections 1.3, 3.3 and 3.4). Jobs are locked FOR UPDATE SKIP LOCKED; the workers row is read in
// the same statement without a lock.
func assignRevoke(ctx context.Context, tx pgx.Tx, batchLimit int, res *storage.AssignResult) error {
	if batchLimit <= 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT j.id, j.assigned_worker_id, (w.status IS DISTINCT FROM 'HEALTHY')
		FROM jobs j
		LEFT JOIN workers w ON w.id = j.assigned_worker_id
		WHERE j.assigned_worker_id IS NOT NULL
		  AND (w.status IS DISTINCT FROM 'HEALTHY' OR j.assignment_expires_at <= clock_timestamp())
		ORDER BY j.created_at ASC, j.id ASC
		LIMIT $1
		FOR UPDATE OF j SKIP LOCKED;`, batchLimit)
	if err != nil {
		return mapPgError(err)
	}
	var revokes []revokeRow
	for rows.Next() {
		var rr revokeRow
		if err := rows.Scan(&rr.jobID, &rr.workerID, &rr.unhealthy); err != nil {
			rows.Close()
			return mapPgError(err)
		}
		revokes = append(revokes, rr)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return mapPgError(err)
	}

	for _, rr := range revokes {
		if rr.unhealthy {
			if _, err := tx.Exec(ctx, `
				UPDATE jobs SET assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL
				WHERE id = $1;`, rr.jobID); err != nil {
				return mapPgError(err)
			}
			if err := insertAssignmentEvent(ctx, tx, rr.jobID, domain.JobEventAssignmentRevoked,
				map[string]any{"worker_id": rr.workerID, "reason": "worker_unhealthy"}); err != nil {
				return err
			}
			res.Revoked++
			continue
		}
		var lapses int
		if err := tx.QueryRow(ctx, `
			UPDATE jobs SET assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL,
			                assignment_lapses = assignment_lapses + 1
			WHERE id = $1
			RETURNING assignment_lapses;`, rr.jobID).Scan(&lapses); err != nil {
			return mapPgError(err)
		}
		if err := insertAssignmentEvent(ctx, tx, rr.jobID, domain.JobEventAssignmentLapsed,
			map[string]any{"worker_id": rr.workerID, "lapses": lapses, "counted": true}); err != nil {
			return err
		}
		res.Lapsed++
	}
	return nil
}

// assignReserve reserves dispatchable unassigned jobs per task type in FIFO order (ADR-0017
// Sections 2.1 to 2.5). It returns the cursors this pass changed.
func assignReserve(ctx context.Context, tx pgx.Tx, p storage.AssignParams, res *storage.AssignResult) (map[string]string, error) {
	changed := make(map[string]string)
	if p.BatchLimit <= 0 {
		return changed, nil
	}

	// Task types in the order each first appears among the candidates (created_at, id), as the
	// in-memory pass orders them.
	typeRows, err := tx.Query(ctx, `
		SELECT task_type FROM (
			SELECT DISTINCT ON (task_type) task_type, created_at, id
			FROM jobs
			WHERE assigned_worker_id IS NULL AND assignment_lapses < $1 AND (state = 'QUEUED' OR (state = 'RETRYING' AND (not_before IS NULL OR not_before <= clock_timestamp())))
			ORDER BY task_type, created_at ASC, id ASC
		) heads
		ORDER BY created_at ASC, id ASC;`, p.MaxLapses)
	if err != nil {
		return nil, mapPgError(err)
	}
	var types []string
	for typeRows.Next() {
		var ty string
		if err := typeRows.Scan(&ty); err != nil {
			typeRows.Close()
			return nil, mapPgError(err)
		}
		types = append(types, ty)
	}
	typeRows.Close()
	if err := typeRows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	// Workers whose row lock timed out are skipped for the rest of the pass (Section 2.4).
	skipped := make(map[string]bool)
	for _, ty := range types {
		cursor := ""
		var last *string
		err := tx.QueryRow(ctx, `SELECT last_worker_id FROM assignment_cursor WHERE task_type = $1;`, ty).Scan(&last)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, mapPgError(err)
		}
		if last != nil {
			cursor = *last
		}

		// The per-type candidate read: at most BatchLimit, FIFO, SKIP LOCKED (D14). A job at the
		// lapse limit has left round-robin (implementation note 1).
		candRows, err := tx.Query(ctx, `
			SELECT id FROM jobs
			WHERE task_type = $1 AND assigned_worker_id IS NULL AND assignment_lapses < $2 AND (state = 'QUEUED' OR (state = 'RETRYING' AND (not_before IS NULL OR not_before <= clock_timestamp())))
			ORDER BY created_at ASC, id ASC
			LIMIT $3
			FOR UPDATE SKIP LOCKED;`, ty, p.MaxLapses, p.BatchLimit)
		if err != nil {
			return nil, mapPgError(err)
		}
		var candidates []string
		for candRows.Next() {
			var id string
			if err := candRows.Scan(&id); err != nil {
				candRows.Close()
				return nil, mapPgError(err)
			}
			candidates = append(candidates, id)
		}
		candRows.Close()
		if err := candRows.Err(); err != nil {
			return nil, mapPgError(err)
		}
		res.CandidatesRead[ty] = len(candidates)

		for _, jobID := range candidates {
			selected, eligible, ok, err := assignChooseWorker(ctx, tx, ty, cursor, p, skipped)
			if err != nil {
				return nil, err
			}
			if !ok {
				// E(t) is empty: the rest of this type's batch waits for the next pass (Section 2.5).
				break
			}
			var expires time.Time
			if err := tx.QueryRow(ctx, `
				UPDATE jobs
				SET assigned_worker_id = $2, assigned_at = c.n,
				    assignment_expires_at = c.n + make_interval(secs => $3::double precision)
				FROM (SELECT clock_timestamp() AS n) c
				WHERE jobs.id = $1
				RETURNING jobs.assignment_expires_at;`, jobID, selected, p.AcceptWindow.Seconds()).Scan(&expires); err != nil {
				return nil, mapPgError(err)
			}
			capped := eligible
			if len(capped) > assignmentEventCandidateCap {
				capped = capped[:assignmentEventCandidateCap]
			}
			if err := insertAssignmentEvent(ctx, tx, jobID, domain.JobEventAssigned, map[string]any{
				"worker_id":       selected,
				"expires_at":      expires.UTC(),
				"policy":          domain.DispatchPolicyRoundRobin,
				"task_type":       ty,
				"cursor_before":   cursor,
				"selected":        selected,
				"candidates":      capped,
				"candidate_count": len(eligible),
				"reason":          "round_robin",
			}); err != nil {
				return nil, err
			}
			cursor = selected
			changed[ty] = selected
			res.Reserved++
		}
	}
	return changed, nil
}

// assignChooseWorker makes one round-robin decision for task type ty (ADR-0017 Sections 2.3 and
// 2.4). It reads the eligible set (a snapshot), selects the successor of the cursor, then takes
// that worker's row FOR UPDATE under lock_timeout and re-runs the eligibility check under the
// lock. A worker whose lock times out is skipped for the rest of the pass; a worker that fails
// the re-check is dropped from this decision. Either way the decision moves to the next
// successor. It returns the eligible set of the successful decision, sorted by ID in byte order,
// for the event payload.
//
// Eligibility (Section 2.2), with $1 the task type, $2 the worker timeout and $3 the accept window
// in seconds and $4 domain.EffectiveWorkerSlots: HEALTHY, supports the type, fresh heartbeat, fresh
// poll, one effective slot (no PENDING or RUNNING attempt, Section 2.4), capacity left, and no live
// reservation. Inside the pass transaction the reservation check also sees this pass's own
// reservations, so a worker reserved earlier in the pass is not eligible again. The snapshot read
// and the re-check under the lock use the same predicate.
func assignChooseWorker(ctx context.Context, tx pgx.Tx, ty, cursor string, p storage.AssignParams, skipped map[string]bool) (string, []string, bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT w.id FROM workers w
		WHERE
			w.status = 'HEALTHY'
			AND $1::text = ANY(w.supported_tasks)
			AND w.last_heartbeat_at > clock_timestamp() - make_interval(secs => $2::double precision)
			AND w.last_poll_at IS NOT NULL
			AND w.last_poll_at > clock_timestamp() - make_interval(secs => $3::double precision)
			AND (SELECT COUNT(*) FROM attempts a WHERE a.worker_id = w.id AND a.state IN ('PENDING', 'RUNNING')) < $4
			AND w.total_slots - (SELECT COUNT(*) FROM attempts a WHERE a.worker_id = w.id AND a.state IN ('PENDING', 'RUNNING')) > 0
			AND NOT EXISTS (
				SELECT 1 FROM jobs rj
				WHERE rj.assigned_worker_id = w.id AND rj.assignment_expires_at > clock_timestamp());`,
		ty, p.WorkerTimeout.Seconds(), p.AcceptWindow.Seconds(), domain.EffectiveWorkerSlots)
	if err != nil {
		return "", nil, false, mapPgError(err)
	}
	var eligible []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return "", nil, false, mapPgError(err)
		}
		if !skipped[id] {
			eligible = append(eligible, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", nil, false, mapPgError(err)
	}
	sort.Strings(eligible)

	remaining := append([]string(nil), eligible...)
	for len(remaining) > 0 {
		next, ok := scheduler.NextAfterCursor(cursor, remaining)
		if !ok {
			break
		}
		lockedOK, err := assignLockAndRecheck(ctx, tx, next, ty, p)
		if err != nil {
			return "", nil, false, err
		}
		if lockedOK == recheckEligible {
			return next, eligible, true, nil
		}
		if lockedOK == recheckLockTimeout {
			skipped[next] = true
		}
		remaining = removeString(remaining, next)
		eligible = removeString(eligible, next)
	}
	return "", nil, false, nil
}

type recheckOutcome int

const (
	recheckEligible recheckOutcome = iota
	recheckIneligible
	recheckLockTimeout
)

// assignLockAndRecheck takes the worker row lock inside a savepoint, so a lock timeout rolls back
// only that statement and the pass transaction stays usable. Under the lock it re-reads the
// eligibility, including a fresh read of the worker's attempts (Section 4.3).
func assignLockAndRecheck(ctx context.Context, tx pgx.Tx, workerID, ty string, p storage.AssignParams) (recheckOutcome, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0, mapPgError(err)
	}
	var id string
	err = sp.QueryRow(ctx, `SELECT id FROM workers WHERE id = $1 FOR UPDATE;`, workerID).Scan(&id)
	if err != nil {
		_ = sp.Rollback(ctx)
		if isLockNotAvailable(err) {
			return recheckLockTimeout, nil
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return recheckIneligible, nil
		}
		return 0, mapPgError(err)
	}
	if err := sp.Commit(ctx); err != nil {
		return 0, mapPgError(err)
	}
	var eligible bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM workers w
			WHERE w.id = $5 AND
			w.status = 'HEALTHY'
			AND $1::text = ANY(w.supported_tasks)
			AND w.last_heartbeat_at > clock_timestamp() - make_interval(secs => $2::double precision)
			AND w.last_poll_at IS NOT NULL
			AND w.last_poll_at > clock_timestamp() - make_interval(secs => $3::double precision)
			AND (SELECT COUNT(*) FROM attempts a WHERE a.worker_id = w.id AND a.state IN ('PENDING', 'RUNNING')) < $4
			AND w.total_slots - (SELECT COUNT(*) FROM attempts a WHERE a.worker_id = w.id AND a.state IN ('PENDING', 'RUNNING')) > 0
			AND NOT EXISTS (
				SELECT 1 FROM jobs rj
				WHERE rj.assigned_worker_id = w.id AND rj.assignment_expires_at > clock_timestamp()));`,
		ty, p.WorkerTimeout.Seconds(), p.AcceptWindow.Seconds(), domain.EffectiveWorkerSlots, workerID).Scan(&eligible)
	if err != nil {
		return 0, mapPgError(err)
	}
	if !eligible {
		return recheckIneligible, nil
	}
	return recheckEligible, nil
}

func removeString(list []string, s string) []string {
	out := list[:0:0]
	for _, v := range list {
		if v != s {
			out = append(out, v)
		}
	}
	return out
}

func insertAssignmentEvent(ctx context.Context, tx pgx.Tx, jobID string, eventType domain.JobEventType, details map[string]any) error {
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("marshal %s details: %w", eventType, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO job_events (job_id, event_type, details, created_at)
		VALUES ($1, $2, $3, clock_timestamp());`, jobID, eventType, raw); err != nil {
		return mapPgError(err)
	}
	return nil
}

// ClaimForPoll claims a job for a poll by workerID (ADR-0017 Sections 3.2 and 3.5) in one
// transaction through the dispatch core of DispatchNextJob: the own live reservation first, then
// the fallback arms in FIFO order (the lapse arm, and the age arm, which needs opts.AgeArmOpen,
// the job dispatchable for FallbackAge and the pass silent for FallbackAge, both on database
// time). Error precedence matches the in-memory repository: ErrWorkerNotFound,
// ErrWorkerUnhealthy, ErrWorkerNoCapacity, then ErrNoQueuedJobs.
func (r *Repository) ClaimForPoll(ctx context.Context, workerID string, opts storage.ClaimOptions) (*storage.AssignedClaim, error) {
	var (
		statusStr      string
		availableSlots int
		supportedTasks []string
	)
	err := r.pool.QueryRow(ctx, `SELECT status, available_slots, supported_tasks FROM worker_capacity WHERE id = $1;`, workerID).
		Scan(&statusStr, &availableSlots, &supportedTasks)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWorkerNotFound
		}
		return nil, mapPgError(err)
	}
	if statusStr != string(domain.WorkerStatusHealthy) {
		return nil, domain.ErrWorkerUnhealthy
	}
	if availableSlots <= 0 {
		// The poll still reached a HEALTHY worker: record its freshness (Section 2.2).
		if _, err := r.pool.Exec(ctx, touchPollSQL, workerID, assignmentPollWriteThrottleSeconds); err != nil {
			return nil, mapPgError(err)
		}
		return nil, domain.ErrWorkerNoCapacity
	}

	claim, err := r.claimTx(ctx, workerID, supportedTasks, opts)
	if errors.Is(err, domain.ErrNoQueuedJobs) {
		// A 204 poll: the freshness write is its own statement and holds no other lock (Section 4.3).
		if _, werr := r.pool.Exec(ctx, touchPollSQL, workerID, assignmentPollWriteThrottleSeconds); werr != nil {
			return nil, mapPgError(werr)
		}
	}
	return claim, err
}

func (r *Repository) claimTx(ctx context.Context, workerID string, supportedTasks []string, opts storage.ClaimOptions) (*storage.AssignedClaim, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer tx.Rollback(ctx)

	var (
		job      domain.Job
		jobState string
		policy   = domain.DispatchPolicyRoundRobin
		reason   string
	)
	// Arm 1: own live reservation (Section 3.2 step 1).
	err = tx.QueryRow(ctx, `
		SELECT id, task_type, payload, state, max_attempts, timeout_seconds, created_at, updated_at
		FROM jobs
		WHERE assigned_worker_id = $1
		  AND assignment_expires_at > clock_timestamp()
		  AND task_type = ANY($2::text[])
		  AND (state = 'QUEUED' OR (state = 'RETRYING' AND (not_before IS NULL OR not_before <= clock_timestamp())))
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED;`, workerID, supportedTasks).
		Scan(&job.ID, &job.TaskType, &job.Payload, &jobState, &job.MaxAttempts, &job.TimeoutSeconds, &job.CreatedAt, &job.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		// Arm 2: fallback (Section 3.2 step 2). No live reservation of anyone, and either the lapse
		// limit, or the age arm: this poll's own refresh failed, the job has been dispatchable for
		// FallbackAge and the pass has been silent for FallbackAge.
		var byLapses bool
		err = tx.QueryRow(ctx, `
			SELECT id, task_type, payload, state, max_attempts, timeout_seconds, created_at, updated_at, (assignment_lapses >= $2)
			FROM jobs
			WHERE task_type = ANY($1::text[])
			  AND (state = 'QUEUED' OR (state = 'RETRYING' AND (not_before IS NULL OR not_before <= clock_timestamp())))
			  AND (assigned_worker_id IS NULL OR assignment_expires_at <= clock_timestamp())
			  AND (assignment_lapses >= $2
			       OR ($3::boolean
			           AND GREATEST(created_at, COALESCE(not_before, created_at))
			               <= clock_timestamp() - make_interval(secs => $4::double precision)
			           AND (SELECT last_completed_at FROM assignment_pass_state WHERE singleton)
			               <= clock_timestamp() - make_interval(secs => $4::double precision)))
			ORDER BY created_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED;`, supportedTasks, opts.MaxLapses, opts.AgeArmOpen, opts.FallbackAge.Seconds()).
			Scan(&job.ID, &job.TaskType, &job.Payload, &jobState, &job.MaxAttempts, &job.TimeoutSeconds, &job.CreatedAt, &job.UpdatedAt, &byLapses)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNoQueuedJobs
		}
		if err != nil {
			return nil, mapPgError(err)
		}
		policy = domain.DispatchPolicyRoundRobinFallback
		reason = domain.FallbackReasonAge
		if byLapses {
			reason = domain.FallbackReasonLapses
		}
	} else if err != nil {
		return nil, mapPgError(err)
	}
	job.State = domain.JobState(jobState)
	fromState := jobState
	if err := domain.ValidateJobTransition(job.State, domain.StateRunning); err != nil {
		return nil, err
	}

	// Worker row lock (jobs is already locked) and the re-checks under it, as DispatchNextJob.
	var (
		totalSlots   int
		workerStatus string
		lockedTasks  []string
	)
	err = tx.QueryRow(ctx, `SELECT total_slots, status, supported_tasks FROM workers WHERE id = $1 FOR UPDATE;`, workerID).
		Scan(&totalSlots, &workerStatus, &lockedTasks)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrWorkerNotFound
		}
		return nil, mapPgError(err)
	}
	if workerStatus != string(domain.WorkerStatusHealthy) {
		return nil, domain.ErrWorkerUnhealthy
	}
	supported := false
	for _, st := range lockedTasks {
		if st == job.TaskType {
			supported = true
			break
		}
	}
	if !supported {
		return nil, domain.ErrWorkerTaskUnsupported
	}
	var activeWorkerAttempts int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM attempts WHERE worker_id = $1 AND state IN ('PENDING', 'RUNNING');`, workerID).Scan(&activeWorkerAttempts); err != nil {
		return nil, mapPgError(err)
	}
	if totalSlots-activeWorkerAttempts <= 0 {
		return nil, domain.ErrWorkerNoCapacity
	}
	var activeJobAttempts int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM attempts WHERE job_id = $1 AND state IN ('PENDING', 'RUNNING');`, job.ID).Scan(&activeJobAttempts); err != nil {
		return nil, mapPgError(err)
	}
	if activeJobAttempts > 0 {
		return nil, domain.ErrInvalidRequest
	}
	// The claim's freshness write, inside the claim transaction under the worker row lock (Section 2.2).
	if _, err := tx.Exec(ctx, touchPollSQL, workerID, assignmentPollWriteThrottleSeconds); err != nil {
		return nil, mapPgError(err)
	}

	var nextAttemptNo int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(attempt_no), 0) + 1 FROM attempts WHERE job_id = $1;`, job.ID).Scan(&nextAttemptNo); err != nil {
		return nil, mapPgError(err)
	}
	rawLeaseToken, err := domain.NewLeaseToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate secure lease token: %w", err)
	}
	tokenHash := domain.HashLeaseToken(rawLeaseToken)
	attemptID := fmt.Sprintf("att_%s_%d", job.ID, nextAttemptNo)

	var startedAt, leaseExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO attempts (id, job_id, attempt_no, worker_id, lease_token_hash, state, started_at, lease_expires_at)
		VALUES ($1, $2, $3, $4, $5, 'RUNNING', clock_timestamp(),
		        clock_timestamp() + make_interval(secs => $6::double precision))
		RETURNING started_at, lease_expires_at;`,
		attemptID, job.ID, nextAttemptNo, workerID, tokenHash, r.leaseDuration.Seconds()).Scan(&startedAt, &leaseExpiresAt)
	if err != nil {
		return nil, mapPgError(err)
	}

	// The claim: RUNNING, reservation cleared, lapses reset, the fallback reason recorded (never
	// cleared). The state predicate guards the transition (INV-1, INV-8).
	var reasonParam *string
	if reason != "" {
		reasonParam = &reason
	}
	var updatedAt time.Time
	var fallbackReason *string
	err = tx.QueryRow(ctx, `
		UPDATE jobs
		SET state = 'RUNNING', current_attempt_id = $2, not_before = NULL, updated_at = clock_timestamp(),
		    assigned_worker_id = NULL, assigned_at = NULL, assignment_expires_at = NULL, assignment_lapses = 0,
		    assignment_fallback_reason = COALESCE($3, assignment_fallback_reason)
		WHERE id = $1 AND state IN ('QUEUED', 'RETRYING')
		RETURNING updated_at, assignment_fallback_reason;`, job.ID, attemptID, reasonParam).Scan(&updatedAt, &fallbackReason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrNoQueuedJobs
		}
		return nil, mapPgError(err)
	}

	details := map[string]any{"worker_id": workerID, "attempt_no": nextAttemptNo, "policy": policy}
	if reason != "" {
		details["fallback_reason"] = reason
	}
	rawDetails, _ := json.Marshal(details)
	if _, err := tx.Exec(ctx, `
		INSERT INTO job_events (job_id, attempt_id, event_type, from_state, to_state, details, created_at)
		VALUES ($1, $2, $3, $4, 'RUNNING', $5, clock_timestamp());`,
		job.ID, attemptID, domain.JobEventDispatched, fromState, rawDetails); err != nil {
		return nil, mapPgError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}

	job.State = domain.StateRunning
	job.CurrentAttemptID = attemptID
	job.UpdatedAt = updatedAt
	job.AssignmentFallbackReason = fallbackReason
	return &storage.AssignedClaim{
		Job: &job,
		Attempt: &domain.Attempt{
			ID:             attemptID,
			JobID:          job.ID,
			AttemptNo:      nextAttemptNo,
			WorkerID:       workerID,
			LeaseToken:     rawLeaseToken,
			LeaseTokenHash: tokenHash,
			State:          domain.AttemptRunning,
			StartedAt:      startedAt,
			LeaseExpiresAt: &leaseExpiresAt,
		},
		Policy:         policy,
		FallbackReason: reason,
	}, nil
}
