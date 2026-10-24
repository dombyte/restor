#!/bin/sh
# restor hook: reports each job to Healthchecks (healthchecks.io or self-hosted), so a
# backup that fails, hangs or stops running at all raises an alert.
#
# Use it as pre and post hook (e.g. in defaults, then every job reports):
#   pre: /etc/restor/hooks/healthchecks.sh
#   post: /etc/restor/hooks/healthchecks.sh
# pre pings /start (Healthchecks measures the duration and notices a hang), post pings
# success or /fail with restor's error. An incomplete snapshot (unreadable files) is
# reported as a failure. The check is the job name as slug in the project of HC_PING_KEY
# (hooks.conf); an argument names another slug.
#
# Or one check for the whole run, as the top-level post hook (slug "restor" by default):
#   post: /etc/restor/hooks/healthchecks.sh
# It reports success only when every job backed up completely, else the failure with the
# failed and incomplete jobs.
#
# The ping key comes from hooks.conf, not from an argument: restor logs a hook's
# arguments at debug level. A failed ping is reported on stderr but never fails the hook,
# so an outage of Healthchecks does not stop the backup.
set -eu

conf=${RESTOR_HOOKS_CONF:-/etc/restor/hooks.conf}
# shellcheck source=hooks.conf
[ -r "$conf" ] && . "$conf"

: "${HC_PING_KEY:?healthchecks: HC_PING_KEY is not set in $conf}"
base="${HC_URL:-https://hc-ping.com}/$HC_PING_KEY/${1:-${RESTOR_JOB:-restor}}"
query=""
[ "${HC_CREATE:-0}" = 1 ] && query="?create=1"

# report sends body (at most 10 kB are kept by Healthchecks) to the base URL + suffix.
report() {
	printf '%s' "$2" | head -c 10000 |
		curl --fail --silent --show-error --max-time 10 --retry 3 \
			--data-binary @- --output /dev/null "$base$1$query" ||
		echo "healthchecks: ping for ${RESTOR_JOB:-the run} failed" >&2
}

if [ -z "${RESTOR_JOB:-}" ]; then
	# The top-level post or on_failure hook: one check for the whole run.
	case "$RESTOR_STATUS" in
	success) report "" "backed up: $RESTOR_SUCCEEDED_JOBS" ;;
	*) report /fail "failed: $RESTOR_FAILED_JOBS
incomplete: $RESTOR_INCOMPLETE_JOBS
${RESTOR_ERROR:-}" ;;
	esac
	exit 0
fi

case "$RESTOR_HOOK" in
pre)
	report /start ""
	;;
post | on_failure)
	case "$RESTOR_STATUS" in
	success) report "" "snapshot ${RESTOR_SNAPSHOT_ID:-unchanged}" ;;
	*) report /fail "$RESTOR_STATUS: ${RESTOR_ERROR:-snapshot ${RESTOR_SNAPSHOT_ID:-}}" ;;
	esac
	;;
esac
