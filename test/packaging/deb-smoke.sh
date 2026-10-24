#!/usr/bin/env bash
# Roadmap T-0.17: the qs-node .deb installs and starts under systemd on
# Debian 13, runs as the unprivileged qstack user, serves /healthz and is
# removed cleanly. Needs Docker; run after `make deb`.
set -euo pipefail

deb=$(ls dist/qs-node_*.deb | head -1)
docker build -q -t qstack-systemd-test -f test/packaging/systemd.Dockerfile test/packaging >/dev/null
cid=$(docker run -d --privileged --cgroupns=host --tmpfs /run --tmpfs /run/lock \
	-v /sys/fs/cgroup:/sys/fs/cgroup:rw qstack-systemd-test)
cleanup() {
	status=$?
	if [ $status -ne 0 ]; then docker exec "$cid" journalctl -u qs-node --no-pager | tail -50 || true; fi
	docker rm -f "$cid" >/dev/null
	exit $status
}
trap cleanup EXIT
ctr() { docker exec "$cid" "$@"; }

for _ in $(seq 60); do
	state=$(ctr systemctl is-system-running 2>/dev/null || true)
	[ "$state" = running ] || [ "$state" = degraded ] && break
	sleep 1
done

# Not /tmp: systemd mounts a fresh tmpfs over it, hiding copied files.
docker cp "$deb" "$cid:/root/qs-node.deb"
ctr apt-get install -y -q /root/qs-node.deb >/dev/null

for _ in $(seq 30); do ctr systemctl is-active -q qs-node && break; sleep 1; done
ctr systemctl is-active qs-node
ctr curl -fsS http://127.0.0.1:9101/healthz; echo

pid=$(ctr systemctl show -p MainPID --value qs-node)
uid=$(ctr awk '/^Uid:/ {print $2}' "/proc/$pid/status")
[ "$uid" = "$(ctr id -u qstack)" ] || { echo "qs-node runs as uid $uid, want qstack"; exit 1; }
[ "$(ctr stat -c %a /etc/qstack/qs-node.yaml)" = 640 ] || { echo "config mode is not 0640"; exit 1; }
ctr systemd-analyze security qs-node --no-pager | tail -1

ctr apt-get remove -y -q qs-node >/dev/null
if ctr systemctl is-active -q qs-node; then echo "qs-node still active after removal"; exit 1; fi
echo "deb-smoke: ok"
