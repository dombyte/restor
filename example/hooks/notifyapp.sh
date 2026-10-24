#!/bin/sh
# restor hook: push notifications to the Notify app (getnotifyapp.com) on iPhone, built
# from the RESTOR_* variables of the hook it runs as.
#
# One message per run, with every job's result (the usual choice):
#   post: /etc/restor/hooks/notifyapp.sh          # top level, not under defaults
#     "❌ restor on host: 1 of 3 jobs failed"
#     ✅ db: snapshot 0123abcd / ✅ etc: unchanged / ❌ web: failed, then the errors
#
# Or one message per job and hook, under defaults:
#   pre: /etc/restor/hooks/notifyapp.sh    # "db: backup started"
#   post: /etc/restor/hooks/notifyapp.sh   # "db: backed up", "backup incomplete", "failed"
#
# The top-level post runs after every run, on_failure only after a failed one: use one of
# them, not both (a failed run would send two messages). NOTIFY_LEVEL=warning in
# hooks.conf drops starts and successes. The messages of one host share a thread
# (groupType restor-<host>).
#
# NOTIFYAPP_ID (a device ID, or a group ID starting with GRP) and NOTIFYAPP_TOKEN come
# from hooks.conf, not from arguments: restor logs a hook's arguments at debug level.
# API: https://getnotifyapp.com/apidocs/ (POST /notify-json/{id}).
set -eu

conf=${RESTOR_HOOKS_CONF:-/etc/restor/hooks.conf}
# shellcheck source=hooks.conf
[ -r "$conf" ] && . "$conf"
: "${NOTIFYAPP_ID:?notifyapp: NOTIFYAPP_ID is not set in $conf}"
: "${NOTIFYAPP_TOKEN:?notifyapp: NOTIFYAPP_TOKEN is not set in $conf}"

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
		level=info
		title="✅ restor on $host: all $jobs jobs backed up"
		;;
	incomplete)
		level=warning
		title="⚠️ restor on $host: $(count "$RESTOR_INCOMPLETE_JOBS") of $jobs backups incomplete"
		;;
	*)
		level=error
		title="🚨 restor on $host: $(count "$RESTOR_FAILED_JOBS") of $jobs jobs failed"
		[ -n "$RESTOR_FAILED_JOBS" ] || title="🚨 restor on $host: maintenance failed"
		;;
	esac
	message=$(summary)
elif [ "$RESTOR_HOOK" = pre ]; then
	level=info
	title="⏳ $RESTOR_JOB: backup started"
	message="restor on $host is backing up $RESTOR_JOB."
else
	short=$(printf '%.8s' "${RESTOR_SNAPSHOT_ID:-}")
	case "$RESTOR_STATUS" in
	success)
		level=info
		title="✅ $RESTOR_JOB: backed up"
		message="Snapshot $short saved on $host."
		[ -n "$short" ] || message="Nothing changed on $host since the last snapshot."
		;;
	incomplete)
		level=warning
		title="⚠️ $RESTOR_JOB: backup incomplete"
		message="Snapshot ${short:-without ID} on $host misses files restic could not read."
		;;
	*)
		level=error
		title="❌ $RESTOR_JOB: backup failed"
		message=${RESTOR_ERROR:-unknown error}
		;;
	esac
fi

# rank orders the levels, so NOTIFY_LEVEL can drop the less important ones.
rank() { case "$1" in info) echo 0 ;; warning) echo 1 ;; *) echo 2 ;; esac }
[ "$(rank "$level")" -ge "$(rank "${NOTIFY_LEVEL:-info}")" ] || exit 0

# json prints $1 as a JSON string: backslashes, quotes, tabs and line breaks escaped.
json() {
	printf '%s' "$1" | awk 'BEGIN { ORS = ""; printf "\"" }
		{ gsub(/\\/, "\\\\"); gsub(/"/, "\\\""); gsub(/\t/, "\\t"); gsub(/\r/, "")
		  if (NR > 1) printf "\\n"; print }
		END { printf "\"" }'
}

# Notify takes 16 KB of text; a push shows the first 4 KB, the app keeps all of it.
message=$(printf '%s' "$message" | head -c 15000)
printf '{"title":%s,"text":%s,"groupType":%s}' \
	"$(json "$title")" "$(json "$message")" "$(json "restor-$host")" |
	curl --fail --silent --show-error --max-time 10 --retry 3 --output /dev/null \
		-H "Content-Type: application/json" --data-binary @- \
		"https://push.getnotifyapp.com/notify-json/$NOTIFYAPP_ID?token=$NOTIFYAPP_TOKEN"
