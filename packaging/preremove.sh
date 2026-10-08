#!/bin/sh
# On removal (deb: "remove", rpm: 0 packages left), not on upgrade, disable the system
# timer. A running backup is left alone so it can restart the services it stopped.
set -e
case "$1" in
remove | 0)
	if [ -d /run/systemd/system ]; then
		systemctl disable --now restor.timer >/dev/null 2>&1 || true
	fi
	;;
esac
