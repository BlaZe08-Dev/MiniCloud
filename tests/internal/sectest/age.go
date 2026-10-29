package sectest

import "time"

// AgeWorkers makes every worker's last heartbeat older than d (memory: fake clock jump;
// postgres: last_heartbeat_at moved back on the isolated schema).
func (r *Rig) AgeWorkers(d time.Duration) {
	r.T.Helper()
	if r.Clock != nil {
		r.Clock.Advance(d)
		return
	}
	r.PG.Exec(r.T, "UPDATE workers SET last_heartbeat_at = clock_timestamp() - make_interval(secs => $1::double precision)", d.Seconds())
}
