#!/usr/bin/env bash
# Removes the L0 stand and its volumes. Used by `make dev-down`.
set -euo pipefail

ns=${L0_NAMESPACE:-qstack-l0}
release=${L0_RELEASE:-qstack}
helm=${HELM:-helm}

$helm uninstall "$release" -n "$ns" --wait --ignore-not-found
# NATS volumes; CloudNativePG removes its own with the cluster.
kubectl -n "$ns" delete pvc -l "app.kubernetes.io/instance=$release" --ignore-not-found
echo "dev-down: removed $release from $ns"
