#!/usr/bin/env bash
# Real smoke test: boots the CI-built image (tender-acl-ci-test) against
# real Postgres/Valkey/SQS-compatible (LocalStack) containers — the same
# docker-compose.yml local development uses — and issues real HTTP requests
# against it, rather than only checking that the binary exits non-zero on
# missing env vars (image-size-check.sh's / the old smoke-tests.sh's own
# startup gate proves config validation fires, but never proves the shipped
# image actually boots and serves a real request end-to-end).
#
# Invoked by ci.yml's smoke job, after tender-acl-ci-test is loaded.
#
# Uses --network host so the app container reaches docker-compose's
# host-mapped ports (5536/6382/4569) exactly the way a locally `go run`
# binary does per .env.example — no separate compose network/DNS wiring
# needed. --network host is supported on GitHub-hosted Ubuntu runners.
set -euo pipefail

CONTAINER_NAME=tender-acl-smoke
COMPOSE_SERVICES=(postgres valkey localstack)

cleanup() {
  echo "::group::Cleanup"
  docker logs "$CONTAINER_NAME" 2>&1 | tail -100 || true
  docker rm -f "$CONTAINER_NAME" >/dev/null 2>&1 || true
  docker compose down -v --remove-orphans >/dev/null 2>&1 || true
  echo "::endgroup::"
}
trap cleanup EXIT

echo "::group::Start dependencies (postgres, valkey, localstack)"
docker compose up -d "${COMPOSE_SERVICES[@]}"
# docker-compose.yml's own healthchecks (pg_isready / valkey-cli ping /
# awslocal sqs list-queues) are the readiness signal here, not a fixed sleep.
for svc in "${COMPOSE_SERVICES[@]}"; do
  cid=$(docker compose ps -q "$svc")
  echo "waiting for $svc to report healthy..."
  for _ in $(seq 1 30); do
    status=$(docker inspect --format='{{.State.Health.Status}}' "$cid" 2>/dev/null || echo "unknown")
    [ "$status" = "healthy" ] && break
    sleep 2
  done
  status=$(docker inspect --format='{{.State.Health.Status}}' "$cid" 2>/dev/null || echo "unknown")
  if [ "$status" != "healthy" ]; then
    echo "::error::$svc did not become healthy in time (status: $status)"
    docker compose logs "$svc" | tail -50
    exit 1
  fi
  echo "$svc is healthy."
done
echo "::endgroup::"

echo "::group::Boot tender-acl-ci-test against real dependencies"
docker run -d --name "$CONTAINER_NAME" --network host \
  -e DATABASE_URL="postgres://tender_acl_app:tender_acl_app_dev_password@localhost:5536/tender_acl?sslmode=disable" \
  -e MIGRATION_DATABASE_URL="postgres://tender_acl:tender_acl@localhost:5536/tender_acl?sslmode=disable" \
  -e VALKEY_ADDR="localhost:6382" \
  -e SQS_QUEUE_URL="http://localhost:4569/000000000000/tenant-lifecycle-tenderacl-q" \
  -e SQS_DLQ_URL="http://localhost:4569/000000000000/tenant-lifecycle-tenderacl-q-dlq" \
  -e MEMBER_REMOVAL_SQS_QUEUE_URL="http://localhost:4569/000000000000/member-removal-tenderacl-q" \
  -e MEMBER_REMOVAL_SQS_DLQ_URL="http://localhost:4569/000000000000/member-removal-tenderacl-q-dlq" \
  -e AWS_REGION="us-east-1" \
  -e AWS_ENDPOINT_URL="http://localhost:4569" \
  -e AWS_ACCESS_KEY_ID="localstack" \
  -e AWS_SECRET_ACCESS_KEY="localstack" \
  -e CORE_INTERNAL_BASE_URL="http://localhost:1" \
  -e ENVIRONMENT="dev" \
  -e HTTP_PORT="8080" \
  -e METRICS_PORT="9090" \
  tender-acl-ci-test
echo "::endgroup::"

echo "::group::Wait for /readyz"
ready=0
for _ in $(seq 1 30); do
  if code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8080/readyz" 2>/dev/null) && [ "$code" = "200" ]; then
    ready=1
    break
  fi
  sleep 2
done
if [ "$ready" -ne 1 ]; then
  echo "::error::tender-acl-ci-test never reported /readyz=200 within 60s — the shipped image did not boot and connect to Postgres/Valkey."
  exit 1
fi
echo "/readyz is healthy."
echo "::endgroup::"

echo "::group::Real HTTP request — /healthz"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://localhost:8080/healthz")
if [ "$code" != "200" ]; then
  echo "::error::GET /healthz returned ${code}, expected 200"
  exit 1
fi
echo "/healthz returned 200."
echo "::endgroup::"

echo "::group::Real HTTP request — TAC-4 (internal authorization check)"
# TAC-4 must never 404 — an unknown grant answers has_access:false with 200,
# exercising the full router -> service -> cache-miss -> repository -> Postgres
# path against a real database, not a mocked one.
FAKE_ID="00000000-0000-0000-0000-000000000000"
RESP=$(curl -s -w '\n%{http_code}' "http://localhost:8080/internal/tenants/${FAKE_ID}/tenders/${FAKE_ID}/acl/${FAKE_ID}")
CODE=$(echo "$RESP" | tail -1)
BODY=$(echo "$RESP" | head -n -1)
if [ "$CODE" != "200" ]; then
  echo "::error::GET TAC-4 returned ${CODE}, expected 200 (TAC-4 must never 404). Body: ${BODY}"
  exit 1
fi
if ! echo "$BODY" | grep -q '"has_access":false'; then
  echo "::error::TAC-4 response did not contain has_access:false for a nonexistent grant. Body: ${BODY}"
  exit 1
fi
echo "TAC-4 correctly returned 200 has_access:false for a nonexistent grant."
echo "::endgroup::"

{
  echo "### Real HTTP smoke test results"
  echo "- ✅ Image boots against real Postgres/Valkey and self-migrates"
  echo "- ✅ /readyz reports healthy"
  echo "- ✅ /healthz returns 200"
  echo "- ✅ TAC-4 (internal authz check) returns 200 has_access:false, never 404"
} >> "$GITHUB_STEP_SUMMARY"
