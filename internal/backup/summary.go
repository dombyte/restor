package backup

import "strings"

// summary is how a whole run ended: the jobs by result and every failure, for the
// run-level hooks.
type summary struct {
	jobs, succeeded, incomplete, failed []string
	// snapshots are the snapshot IDs as job=ID, of the jobs that saved one.
	snapshots []string
	// failures are the maintenance failures, then the job failures.
	failures []string
}

// summarize collects the results of a run and the failures of its maintenance. Jobs that
// never started (after a cancel) are listed in jobs only.
func (m *Manager) summarize(results []result, maintenance []string) summary {
	s := summary{failures: maintenance}
	for i, r := range results {
		name := m.jobs[i].Settings.Name
		s.jobs = append(s.jobs, name)
		switch r.outcome {
		case outcomeComplete:
			s.succeeded = append(s.succeeded, name)
		case outcomeIncomplete:
			s.incomplete = append(s.incomplete, name)
		case outcomeFailed:
			s.failed = append(s.failed, name)
			s.failures = append(s.failures, r.err.Error())
		}
		if r.snapshotID != "" {
			s.snapshots = append(s.snapshots, name+"="+r.snapshotID)
		}
	}
	return s
}

// status is the RESTOR_STATUS of the run: failed when a job or the maintenance failed,
// incomplete when a snapshot is incomplete, else success.
func (s summary) status() string {
	switch {
	case len(s.failures) > 0:
		return statusFailed
	case len(s.incomplete) > 0:
		return statusIncomplete
	default:
		return statusSuccess
	}
}

// env returns the RESTOR_* variables of a run-level hook; RESTOR_ERROR only when
// something failed.
func (s summary) env(kind string) []string {
	env := []string{
		"RESTOR_HOOK=" + kind, "RESTOR_STATUS=" + s.status(),
		"RESTOR_JOBS=" + strings.Join(s.jobs, ","),
		"RESTOR_SUCCEEDED_JOBS=" + strings.Join(s.succeeded, ","),
		"RESTOR_INCOMPLETE_JOBS=" + strings.Join(s.incomplete, ","),
		"RESTOR_FAILED_JOBS=" + strings.Join(s.failed, ","),
		"RESTOR_SNAPSHOT_IDS=" + strings.Join(s.snapshots, ","),
	}
	if len(s.failures) > 0 {
		env = append(env, "RESTOR_ERROR="+strings.Join(s.failures, "; "))
	}
	return env
}
