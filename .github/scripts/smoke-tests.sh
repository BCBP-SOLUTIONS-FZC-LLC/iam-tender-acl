#!/usr/bin/env bash
# Image size + startup gate smoke tests for the CI-built Docker image.
# Invoked by ci.yml (keeps shell operators out of inline YAML run blocks).
set -euo pipefail

echo "::group::Image size check (linux/amd64)"
# arm64 is typically within ±5 MB of amd64 for a distroless Go binary; the limit
# is deliberately generous so it catches regressions (e.g. accidentally COPYing
# third_party build artefacts or embedding test assets), not normal arch variance.
MAX_MB=200
size=$(docker image inspect tender-acl-ci-test --format='{{.Size}}')
mb=$((size / 1024 / 1024))
echo "Image size: ${mb} MB (limit: ${MAX_MB} MB)"
[ "${mb}" -le "${MAX_MB}" ] &
P1=$!
echo "::endgroup::"

echo "::group::Startup gate"
# The binary must exit non-zero on missing required env vars, proving
# cmd/tender-acl/config.go's loadConfig validation actually fires
# (DATABASE_URL and SQS_QUEUE_URL are required; CORE_INTERNAL_BASE_URL has
# a default and is not required, so it is not exercised by this gate).
# timeout 10s kills the container if it hangs instead of exiting.
exit_code=0
timeout 10s docker run --rm tender-acl-ci-test 2>/dev/null || exit_code=$?
echo "Container exit code: ${exit_code} (expected non-zero)"
[ "${exit_code}" -ne 0 ] &
P2=$!
echo "::endgroup::"

wait $P1 || {
  echo "::error file=Dockerfile,title=Image size::Image is ${mb} MB, exceeds ${MAX_MB} MB limit — check COPY instructions for accidental inclusions"
  exit 1
}
wait $P2 || {
  echo "::error file=cmd/tender-acl/config.go,title=Startup gate::Binary exited 0 on missing required env vars — loadConfig must exit non-zero"
  exit 1
}

{
  echo "### Smoke test results"
  echo "- ✅ Startup gate: binary exits ${exit_code} on missing required env vars (loadConfig fires)"
  echo "- 📦 Image size (linux/amd64): **${mb} MB** (limit: ${MAX_MB} MB)"
  echo "- 🔍 [Security (Trivy SARIF results)](https://github.com/${GITHUB_REPOSITORY}/security/code-scanning)"
} >> "$GITHUB_STEP_SUMMARY"
