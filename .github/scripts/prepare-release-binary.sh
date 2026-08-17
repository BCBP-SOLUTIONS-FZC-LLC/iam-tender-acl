#!/usr/bin/env bash
# Cross-compiles the release binary for all target platforms and writes
# per-binary checksums. The artifact directory is the working directory
# when this runs.
#
# This repo builds a single binary (cmd/tender-acl) that runs both the
# HTTP server and the tenant-lifecycle SQS consumer in one process — there
# is no separate reconciler/consumer binary.
#
# Produces for each platform:
#   tender-acl_{version}_{os}_{arch}[.exe]
#   tender-acl_{version}_{os}_{arch}[.exe].sha256
#
# The caller (create-github-release.sh) aggregates the .sha256 files into
# a single checksums.txt with `sha256sum tender-acl_* | grep -v '\.sha256$'`.
set -euo pipefail

: "${RELEASE_TAG:?RELEASE_TAG is required}"

TARGETS=(
  "linux   amd64"
  "linux   arm64"
  "darwin  amd64"
  "darwin  arm64"
  "windows amd64"
)

echo "Building release binaries for tag ${RELEASE_TAG}"

for target in "${TARGETS[@]}"; do
  read -r GOOS GOARCH <<< "$target"
  EXT=""
  [ "${GOOS}" = "windows" ] && EXT=".exe"
  ASSET_NAME="tender-acl_${RELEASE_TAG}_${GOOS}_${GOARCH}${EXT}"

  echo "  → ${GOOS}/${GOARCH}"
  CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" \
    go build -trimpath \
      -ldflags "-s -w -X main.buildVersion=${RELEASE_TAG}" \
      -o "${ASSET_NAME}" \
      ./cmd/tender-acl

  chmod +x "${ASSET_NAME}"
  sha256sum "${ASSET_NAME}" > "${ASSET_NAME}.sha256"
  echo "    ✔  ${ASSET_NAME}"
done

# Write the primary asset name (linux/amd64) for downstream steps that
# need a single canonical file reference.
echo "tender-acl_${RELEASE_TAG}_linux_amd64" > release-asset.name
echo "All binaries prepared."
