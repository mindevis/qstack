#!/usr/bin/env bash
# Roadmap T-0.17: an OCI image is built, pushed to a local registry, signed
# with cosign and its signature verified. The key pair is ephemeral and
# nothing is uploaded to public transparency logs; publishing and keyless
# signing of release images is decided separately. Needs Docker, cosign
# and a registry on localhost:5000.
set -euo pipefail

repo=localhost:5000/qstack
make image BIN=qs-node IMAGE_REPO=$repo VERSION=ci-smoke
docker push -q $repo/qs-node:ci-smoke >/dev/null
ref=$(docker inspect --format '{{index .RepoDigests 0}}' $repo/qs-node:ci-smoke)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
(cd "$work" && COSIGN_PASSWORD= cosign generate-key-pair >/dev/null)
local_only=(--allow-insecure-registry --allow-http-registry)
COSIGN_PASSWORD= cosign sign --yes --key "$work/cosign.key" --use-signing-config=false --tlog-upload=false \
	"${local_only[@]}" "$ref"
cosign verify --key "$work/cosign.pub" --insecure-ignore-tlog=true "${local_only[@]}" "$ref" >/dev/null

# A second key must not verify the signature.
mkdir "$work/other" && (cd "$work/other" && COSIGN_PASSWORD= cosign generate-key-pair >/dev/null)
if cosign verify --key "$work/other/cosign.pub" --insecure-ignore-tlog=true "${local_only[@]}" "$ref" >/dev/null 2>&1; then
	echo "signature verified with a foreign key"; exit 1
fi

# The image runs as non-root and reports its version.
[ "$(docker inspect --format '{{.Config.User}}' "$ref")" = 65532:65532 ]
docker run --rm "$ref" -version
echo "image-sign: ok"
