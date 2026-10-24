#!/bin/sh
set -e
systemd-sysusers /usr/lib/sysusers.d/qstack.conf
chgrp qstack /etc/qstack/qs-node.yaml
if [ -d /run/systemd/system ]; then
	systemctl daemon-reload
	systemctl enable qs-node.service
	systemctl restart qs-node.service
fi
