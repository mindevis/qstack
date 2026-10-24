#!/bin/sh
set -e
if [ -d /run/systemd/system ] && [ "$1" = remove ]; then
	systemctl disable --now qs-node.service || true
fi
