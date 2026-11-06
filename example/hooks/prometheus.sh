#!/bin/sh
# restor hook: writes the result of every run as Prometheus metrics for node_exporter's
# textfile collector, so that alerts fire on failed jobs and on backups that stopped
# happening altogether.
#
# Use it as the top-level post hook (not under defaults, not on_failure):
#   post: /etc/restor/hooks/prometheus.sh
# and start node_exporter with --collector.textfile.directory=<PROM_TEXTFILE_DIR>.
#
# It writes <PROM_TEXTFILE_DIR>/restor.prom (atomically: node_exporter never reads half a
# file) with
#   restor_run_timestamp_seconds                  when the last run ended
#   restor_run_status{status}                     1 for its RESTOR_STATUS: success,
#                                                 incomplete or failed (a job, forget,
#                                                 prune, unlock or check failed)
#   restor_job_status{job,status}                 1 for each job's last result: success,
#                                                 incomplete, failed or not_run
#   restor_job_last_backup_timestamp_seconds{job} the last run that saved the job
#                                                 (complete or incomplete)
#   restor_job_last_success_timestamp_seconds{job} the last complete one
# The timestamps of a job survive the runs in which it failed, and the metrics of jobs
# that a run did not include (restor run web) stay as they were. Delete restor.prom to
# forget a removed job. Example alerts:
#   time() - restor_job_last_success_timestamp_seconds > 2 * 86400
#   restor_run_status{status="failed"} == 1
set -eu

conf=${RESTOR_HOOKS_CONF:-/etc/restor/hooks.conf}
# shellcheck source=hooks.conf
[ -r "$conf" ] && . "$conf"
: "${PROM_TEXTFILE_DIR:?prometheus: PROM_TEXTFILE_DIR is not set in $conf}"

if [ -n "${RESTOR_JOB:-}" ]; then
	echo "prometheus: use this script as the top-level post hook, not as a job's hook" >&2
	exit 1
fi

out="$PROM_TEXTFILE_DIR/restor.prom"
tmp="$out.$$.tmp"
trap 'rm -f "$tmp"' EXIT
previous=/dev/null
[ -r "$out" ] && previous=$out

# The previous file gives the timestamps of jobs that did not back up now, and every
# metric of jobs that were not part of this run. The RESTOR_* variables come from
# ENVIRON.
awk -v now="$(date +%s)" '
function listed(job, list) { return index("," list ",", "," job ",") > 0 }
function label(s) { gsub(/\\/, "\\\\", s); gsub(/"/, "\\\"", s); return s }
function family(name, type, help) {
	printf "# HELP %s %s\n# TYPE %s %s\n", name, help, name, type
}
# add appends job to the job list once.
function add(job) { if (!(job in seen)) { seen[job] = 1; all[++count] = job } }
# timestamp prints the line of family name for a job of this run, when it has a value.
function timestamp(name, job, values) {
	if (job in values) printf "%s{job=\"%s\"} %s\n", name, label(job), values[job]
}
/^restor_job_/ && match($0, /job="([^"\\]|\\.)*"/) {
	job = substr($0, RSTART + 5, RLENGTH - 6)
	gsub(/\\"/, "\"", job); gsub(/\\\\/, "\\", job)
	name = substr($0, 1, index($0, "{") - 1)
	add(job)
	if (!listed(job, ENVIRON["RESTOR_JOBS"])) {
		keep[name, job] = keep[name, job] $0 "\n"
	} else if (name == "restor_job_last_backup_timestamp_seconds") {
		backup[job] = $NF
	} else if (name == "restor_job_last_success_timestamp_seconds") {
		success[job] = $NF
	}
}
END {
	split("success incomplete failed", runStates, " ")
	split("success incomplete failed not_run", jobStates, " ")
	n = split(ENVIRON["RESTOR_JOBS"], jobs, ",")
	for (i = 1; i <= n; i++) {
		job = jobs[i]
		add(job)
		if (listed(job, ENVIRON["RESTOR_SUCCEEDED_JOBS"])) {
			state[job] = "success"; backup[job] = now; success[job] = now
		} else if (listed(job, ENVIRON["RESTOR_INCOMPLETE_JOBS"])) {
			state[job] = "incomplete"; backup[job] = now
		} else if (listed(job, ENVIRON["RESTOR_FAILED_JOBS"])) {
			state[job] = "failed"
		} else {
			state[job] = "not_run"
		}
	}
	# Sorted by name, so that every job keeps its place in the file from run to run.
	for (i = 2; i <= count; i++)
		for (j = i; j > 1 && all[j - 1] > all[j]; j--) {
			t = all[j]; all[j] = all[j - 1]; all[j - 1] = t
		}

	family("restor_run_timestamp_seconds", "gauge", "When the last restor run ended.")
	printf "restor_run_timestamp_seconds %d\n", now
	family("restor_run_status", "gauge", "Result of the last restor run.")
	for (s = 1; s <= 3; s++)
		printf "restor_run_status{status=\"%s\"} %d\n", runStates[s],
			runStates[s] == ENVIRON["RESTOR_STATUS"]
	family("restor_job_status", "gauge", "Result of the last backup of each job.")
	for (i = 1; i <= count; i++) {
		job = all[i]
		if (!(job in state)) { printf "%s", keep["restor_job_status", job]; continue }
		for (s = 1; s <= 4; s++)
			printf "restor_job_status{job=\"%s\",status=\"%s\"} %d\n", label(job),
				jobStates[s], jobStates[s] == state[job]
	}
	name = "restor_job_last_backup_timestamp_seconds"
	family(name, "gauge",
		"When a run last saved a snapshot of the job, complete or incomplete.")
	for (i = 1; i <= count; i++)
		if (all[i] in state) timestamp(name, all[i], backup)
		else printf "%s", keep[name, all[i]]
	name = "restor_job_last_success_timestamp_seconds"
	family(name, "gauge", "When a run last saved a complete snapshot of the job.")
	for (i = 1; i <= count; i++)
		if (all[i] in state) timestamp(name, all[i], success)
		else printf "%s", keep[name, all[i]]
}
' "$previous" >"$tmp"
chmod 0644 "$tmp"
mv "$tmp" "$out"
