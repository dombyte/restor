#!/bin/sh
# Pick up new or changed units. The timer is not enabled: the config must be written first.
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
