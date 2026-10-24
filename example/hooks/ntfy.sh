#!/bin/sh
# restor hook: push notifications through ntfy (ntfy.sh or self-hosted), built from the
# RESTOR_* variables of the hook it runs as.
#
# One message per run, with every job's result (the usual choice):
#   post: /etc/restor/hooks/ntfy.sh          # top level, not under defaults
#     "restor on host: 1 of 3 jobs failed"
#     ✅ db: snapshot 0123abcd / ✅ etc: unchanged / ❌ web: failed, then the errors
#
# Or one message per job and hook, under defaults:
#   pre: /etc/restor/hooks/ntfy.sh    # "db: backup started"
#   post: /etc/restor/hooks/ntfy.sh   # "db: backed up", "backup incomplete", "failed"
#
# The top-level post runs after every run, on_failure only after a failed one: use one of
# them, not both (a failed run would send two messages). The priority follows the
# result; NOTIFY_LEVEL=warning in hooks.conf drops starts and successes.
#
# NTFY_URL (the topic) and NTFY_TOKEN come from hooks.conf, not from arguments: restor
# logs a hook's arguments at debug level.
set -eu

conf=${RESTOR_HOOKS_CONF:-/etc/restor/hooks.conf}
# shellcheck source=hooks.conf
[ -r "$conf" ] && . "$conf"
: "${NTFY_URL:?ntfy: NTFY_URL is not set in $conf}"

host=$(hostname)

# listed reports whether job $1 is in the comma-separated list $2.
listed() { case ",$2," in *",$1,"*) return 0 ;; esac; return 1; }
# count prints the number of entries of the comma-separated list $1.
count() { printf '%s' "$1" | awk -F, '{ n = NF } END { print n + 0 }'; }

# summary prints a line per job of the run with its result, then every failure.
summary() {
	for job in $(printf '%s' "$RESTOR_JOBS" | tr , ' '); do
		id=$(printf '%s' "${RESTOR_SNAPSHOT_IDS:-}" | tr , '\n' |
			awk -F= -v job="$job" '$1 == job { printf "%.8s", $2 }')
		if listed "$job" "${RESTOR_SUCCEEDED_JOBS:-}"; then
			echo "✅ $job: ${id:+snapshot }${id:-unchanged}"
		elif listed "$job" "${RESTOR_INCOMPLETE_JOBS:-}"; then
			echo "⚠️ $job: incomplete${id:+, snapshot $id}"
		elif listed "$job" "${RESTOR_FAILED_JOBS:-}"; then
			echo "❌ $job: failed"
		else
			echo "⏹️ $job: not run"
		fi
	done
	if [ -n "${RESTOR_ERROR:-}" ]; then
		printf '\n%s\n' "$RESTOR_ERROR" | sed 's/; /\n/g'
	fi
}

if [ -z "${RESTOR_JOB:-}" ]; then
	# The top-level post or on_failure hook: the whole run in one message.
	jobs=$(count "$RESTOR_JOBS")
	case "$RESTOR_STATUS" in
	success)
		level=info priority=low tags=white_check_mark
		title="restor on $host: all $jobs jobs backed up"
		;;
	incomplete)
		level=warning priority=default tags=warning
		title="restor on $host: $(count "$RESTOR_INCOMPLETE_JOBS") of $jobs backups incomplete"
		;;
	*)
		level=error priority=high tags=rotating_light
		title="restor on $host: $(count "$RESTOR_FAILED_JOBS") of $jobs jobs failed"
		[ -n "$RESTOR_FAILED_JOBS" ] || title="restor on $host: maintenance failed"
		;;
	esac
	message=$(summary)
elif [ "$RESTOR_HOOK" = pre ]; then
	level=info priority=min tags=hourglass_flowing_sand
	title="$RESTOR_JOB: backup started"
	message="restor on $host is backing up $RESTOR_JOB."
else
	short=$(printf '%.8s' "${RESTOR_SNAPSHOT_ID:-}")
	case "$RESTOR_STATUS" in
	success)
		level=info priority=low tags=white_check_mark
		title="$RESTOR_JOB: backed up"
		message="Snapshot $short saved on $host."
		[ -n "$short" ] || message="Nothing changed on $host since the last snapshot."
		;;
	incomplete)
		level=warning priority=default tags=warning
		title="$RESTOR_JOB: backup incomplete"
		message="Snapshot ${short:-without ID} on $host misses files restic could not read."
		;;
	*)
		level=error priority=high tags=x
		title="$RESTOR_JOB: backup failed"
		message=${RESTOR_ERROR:-unknown error}
		;;
	esac
fi

# rank orders the levels, so NOTIFY_LEVEL can drop the less important ones.
rank() { case "$1" in info) echo 0 ;; warning) echo 1 ;; *) echo 2 ;; esac }
[ "$(rank "$level")" -ge "$(rank "${NOTIFY_LEVEL:-info}")" ] || exit 0

set -- -H "Title: $title" -H "Priority: $priority" -H "Tags: restor,$tags"
[ -n "${NTFY_TOKEN:-}" ] && set -- "$@" -H "Authorization: Bearer $NTFY_TOKEN"

# ntfy keeps 4096 bytes of a message; the end of RESTOR_ERROR matters least.
printf '%s' "$message" | head -c 4000 |
	curl --fail --silent --show-error --max-time 10 --retry 3 --output /dev/null \
		"$@" --data-binary @- "$NTFY_URL"
