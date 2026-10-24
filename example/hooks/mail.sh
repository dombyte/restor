#!/bin/sh
# restor hook: sends a mail through the local sendmail (postfix, msmtp, ...) with what
# happened and every RESTOR_* variable restor set, built for the hook it runs as.
#
# One mail per run, with every job's result (the usual choice):
#   post: /etc/restor/hooks/mail.sh       # top level, not under defaults
# Or one mail per job, under defaults:
#   post: /etc/restor/hooks/mail.sh
#
# The top-level post runs after every run, on_failure only after a failed one: use one of
# them, not both. NOTIFY_LEVEL=warning in hooks.conf mails only incomplete and failed
# backups; MAIL_TO (and MAIL_FROM) come from hooks.conf.
set -eu

conf=${RESTOR_HOOKS_CONF:-/etc/restor/hooks.conf}
# shellcheck source=hooks.conf
[ -r "$conf" ] && . "$conf"
: "${MAIL_TO:?mail: MAIL_TO is not set in $conf}"

host=$(hostname)

# listed reports whether job $1 is in the comma-separated list $2.
listed() { case ",$2," in *",$1,"*) return 0 ;; esac; return 1; }
# count prints the number of entries of the comma-separated list $1.
count() { printf '%s' "$1" | awk -F, '{ n = NF } END { print n + 0 }'; }

# summary prints a line per job of the run with its result.
summary() {
	for job in $(printf '%s' "$RESTOR_JOBS" | tr , ' '); do
		id=$(printf '%s' "${RESTOR_SNAPSHOT_IDS:-}" | tr , '\n' |
			awk -F= -v job="$job" '$1 == job { printf "%.8s", $2 }')
		if listed "$job" "${RESTOR_SUCCEEDED_JOBS:-}"; then
			echo "  $job: backed up, ${id:+snapshot }${id:-unchanged}"
		elif listed "$job" "${RESTOR_INCOMPLETE_JOBS:-}"; then
			echo "  $job: INCOMPLETE${id:+, snapshot $id}"
		elif listed "$job" "${RESTOR_FAILED_JOBS:-}"; then
			echo "  $job: FAILED"
		else
			echo "  $job: not run"
		fi
	done
}

if [ -z "${RESTOR_JOB:-}" ]; then
	# The top-level post or on_failure hook: the whole run in one mail.
	jobs=$(count "$RESTOR_JOBS")
	case "$RESTOR_STATUS" in
	success) level=info subject="all $jobs jobs backed up" ;;
	incomplete)
		level=warning
		subject="$(count "$RESTOR_INCOMPLETE_JOBS") of $jobs backups incomplete"
		;;
	*)
		level=error subject="$(count "$RESTOR_FAILED_JOBS") of $jobs jobs failed"
		[ -n "$RESTOR_FAILED_JOBS" ] || subject="maintenance failed"
		;;
	esac
elif [ "$RESTOR_HOOK" = pre ]; then
	level=info subject="$RESTOR_JOB: backup started"
else
	case "$RESTOR_STATUS" in
	success) level=info subject="$RESTOR_JOB: backed up" ;;
	incomplete) level=warning subject="$RESTOR_JOB: backup incomplete" ;;
	*) level=error subject="$RESTOR_JOB: backup failed" ;;
	esac
fi

# rank orders the levels, so NOTIFY_LEVEL can drop the less important ones.
rank() { case "$1" in info) echo 0 ;; warning) echo 1 ;; *) echo 2 ;; esac }
[ "$(rank "$level")" -ge "$(rank "${NOTIFY_LEVEL:-info}")" ] || exit 0

{
	echo "To: $MAIL_TO"
	echo "From: ${MAIL_FROM:-restor@$host}"
	echo "Subject: [restor $host] $subject"
	echo "Content-Type: text/plain; charset=utf-8"
	echo
	echo "restor on $host: $subject"
	echo
	if [ -n "${RESTOR_JOBS:-}" ]; then
		summary
		echo
	fi
	# RESTOR_ERROR holds every failure, "; "-separated: one per line.
	if [ -n "${RESTOR_ERROR:-}" ]; then
		printf 'Errors:\n%s\n\n' "$RESTOR_ERROR" | sed 's/; /\n/g'
	fi
	echo "Variables of this hook:"
	env | grep '^RESTOR_' | grep -v '^RESTOR_HOOKS_CONF=' | sort | sed 's/^/  /'
	echo
	echo "Log of the run: journalctl -u restor --since today"
} | sendmail -t
