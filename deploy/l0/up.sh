#!/usr/bin/env bash
# Deploys the L0 stand (spec 13 §1.1) with the production chart and
# values-dev.yaml, waits until everything is ready and reports the time.
# Needs kubectl access to the namespace and the CloudNativePG operator in
# the cluster. Used by `make dev-up` (CI: kind; stand: qstack-l0).
#
# Environment: L0_NAMESPACE, L0_RELEASE, L0_IMAGE_REGISTRY, L0_IMAGE_TAG,
# L0_MAX_SECONDS (fail if slower; 0 = no limit), HELM.
set -euo pipefail

ns=${L0_NAMESPACE:-qstack-l0}
release=${L0_RELEASE:-qstack}
helm=${HELM:-helm}
chart=deploy/helm/qstack-control-plane
start=$(date +%s)

args=(upgrade --install "$release" "$chart" -n "$ns" -f "$chart/values-dev.yaml" --wait --timeout 5m)
[ -n "${L0_IMAGE_REGISTRY:-}" ] && args+=(--set "images.registry=$L0_IMAGE_REGISTRY")
[ -n "${L0_IMAGE_TAG:-}" ] && args+=(--set "images.tag=$L0_IMAGE_TAG")
diagnose() {
	echo "dev-up: not ready; pods and recent events:"
	kubectl -n "$ns" get pods -o wide || true
	kubectl -n "$ns" describe pods | grep -A12 -E '^(Name:|Events:)' | tail -80 || true
	kubectl -n "$ns" get events --sort-by=.lastTimestamp | tail -30 || true
}
$helm "${args[@]}" || { diagnose; exit 1; }

# helm --wait does not wait for custom resources.
kubectl -n "$ns" wait --for=condition=Ready "cluster.postgresql.cnpg.io/$release-postgres" --timeout=300s || { diagnose; exit 1; }

elapsed=$(($(date +%s) - start))
echo "dev-up: ready in ${elapsed}s (namespace $ns, release $release)"
kubectl -n "$ns" get pods
if [ "${L0_MAX_SECONDS:-0}" -gt 0 ] && [ "$elapsed" -gt "$L0_MAX_SECONDS" ]; then
	echo "dev-up: took ${elapsed}s, limit ${L0_MAX_SECONDS}s"
	exit 1
fi
