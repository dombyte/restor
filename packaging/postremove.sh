#!/bin/sh
# Forget the removed units.
set -e
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload || true
fi
