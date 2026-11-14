package backup

import "strings"

// Values of RESTOR_COMMAND in the run-level hook environment.
const (
	commandRun      = "run"
	commandMaintain = "maintain"
)

// notes are what went wrong outside of the jobs' own results: in the maintenance, or
// while starting services an earlier run left stopped.
type notes struct {
	failures, warnings []string
}

// add appends the notes of o.
func (n *notes) add(o notes) {
	n.failures = append(n.failures, o.failures...)
	n.warnings = append(n.warnings, o.warnings...)
}

// summary is how a whole run ended: the jobs by result and every failure and warning,
// for the run-level hooks.
type summary struct {
	command                             string
	jobs, succeeded, incomplete, failed []string
	// snapshots are the snapshot IDs as job=ID, of the jobs that saved one.
	snapshots []string
	// failures are the other failures (recovery, maintenance), then the job failures.
	failures []string
	// warnings are the other warnings, then those of the jobs as "job: warning".
	warnings []string
}

// summarize collects the results of a run and the notes from outside the jobs. Jobs that
// never started (after a cancel) are listed in jobs only.
func (m *Manager) summarize(command string, results []result, n notes) summary {
	s := summary{command: command, failures: n.failures, warnings: n.warnings}
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
		for _, w := range r.warnings {
			s.warnings = append(s.warnings, name+": "+w)
		}
	}
	return s
}

// status is the RESTOR_STATUS of the run: failed when a job, the recovery or the
// maintenance failed, incomplete when a snapshot is incomplete, else success.
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
// something failed, RESTOR_WARNINGS only when something warned.
func (s summary) env(kind string) []string {
	env := []string{
		"RESTOR_HOOK=" + kind, "RESTOR_COMMAND=" + s.command, "RESTOR_STATUS=" + s.status(),
		"RESTOR_JOBS=" + strings.Join(s.jobs, ","),
		"RESTOR_SUCCEEDED_JOBS=" + strings.Join(s.succeeded, ","),
		"RESTOR_INCOMPLETE_JOBS=" + strings.Join(s.incomplete, ","),
		"RESTOR_FAILED_JOBS=" + strings.Join(s.failed, ","),
		"RESTOR_SNAPSHOT_IDS=" + strings.Join(s.snapshots, ","),
	}
	if len(s.failures) > 0 {
		env = append(env, "RESTOR_ERROR="+strings.Join(s.failures, warningSeparator))
	}
	if len(s.warnings) > 0 {
		env = append(env, "RESTOR_WARNINGS="+strings.Join(s.warnings, warningSeparator))
	}
	return env
}
