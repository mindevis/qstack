#!/usr/bin/env bash
# Checks a running L0 stand: PostgreSQL accepts connections, NATS has
# JetStream, OpenBao (dev) is unsealed and every fake host's qs-node is
# healthy.
set -euo pipefail

ns=${L0_NAMESPACE:-qstack-l0}
release=${L0_RELEASE:-qstack}
k() { kubectl -n "$ns" "$@"; }

primary=$(k get cluster.postgresql.cnpg.io "$release-postgres" -o jsonpath='{.status.currentPrimary}')
k exec "$primary" -c postgres -- psql -U postgres -d qstack -tAc 'SELECT 1' | grep -qx 1
echo "postgres: ok ($primary)"

k run nats-check --rm -i --restart=Never --image=curlimages/curl:8.22.0 --quiet -- \
	curl -fsS "http://$release-nats-headless:8222/healthz?js-enabled-only=true" >/dev/null
echo "nats: ok (JetStream)"

k run bao-check --rm -i --restart=Never --image=curlimages/curl:8.22.0 --quiet -- \
	curl -fsS "http://$release-openbao:8200/v1/sys/health" | grep -q '"sealed":false'
echo "openbao: ok (dev, unsealed)"

hosts=$(k get pods -l "app.kubernetes.io/instance=$release,app.kubernetes.io/name=fake-host" -o name)
[ -n "$hosts" ] || { echo "no fake hosts"; exit 1; }
for h in $hosts; do
	k wait --for=condition=Ready "$h" --timeout=60s >/dev/null
done
echo "fake hosts: ok ($(echo "$hosts" | wc -l))"
echo "smoke: ok"
