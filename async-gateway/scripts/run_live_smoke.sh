#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source "${ROOT_DIR}/scripts/run_live_smoke_lib.sh"
PG_CONTAINER_NAME="${SMOKE_PG_CONTAINER_NAME:-banana-async-smoke-pg-$RANDOM}"
PG_PORT="${SMOKE_PG_PORT:-55432}"
GATEWAY_ADDR="${SMOKE_GATEWAY_ADDR:-127.0.0.1:18080}"
GATEWAY_URL="http://${GATEWAY_ADDR}"
PG_USER="${SMOKE_PG_USER:-banana}"
PG_PASSWORD="${SMOKE_PG_PASSWORD:-banana}"
PG_DB="${SMOKE_PG_DB:-banana_async_gateway_smoke}"
OWNER_HASH_SECRET="${OWNER_HASH_SECRET:-smoke-owner-secret}"
TASK_PAYLOAD_ENCRYPTION_KEY="${TASK_PAYLOAD_ENCRYPTION_KEY:-MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=}"
NEWAPI_BASE_URL="${SMOKE_NEWAPI_BASE_URL:-}"
SMOKE_API_KEY="${SMOKE_API_KEY:-}"
GATEWAY_LOG_FILE="${SMOKE_GATEWAY_LOG_FILE:-/tmp/banana-async-gateway-smoke.log}"
POSTGRES_DSN="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PG_PORT}/${PG_DB}?sslmode=disable"
GATEWAY_PID=""

if [[ -z "${NEWAPI_BASE_URL}" ]]; then
  echo "SMOKE_NEWAPI_BASE_URL is required" >&2
  exit 1
fi
if [[ -z "${SMOKE_API_KEY}" ]]; then
  echo "SMOKE_API_KEY is required" >&2
  exit 1
fi

cleanup() {
  if [[ -n "${GATEWAY_PID}" ]] && kill -0 "${GATEWAY_PID}" 2>/dev/null; then
    kill "${GATEWAY_PID}" 2>/dev/null || true
    wait "${GATEWAY_PID}" 2>/dev/null || true
  fi
  docker rm -f "${PG_CONTAINER_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

print_gateway_log_tail() {
  if [[ -f "${GATEWAY_LOG_FILE}" ]]; then
    echo "[smoke] gateway log tail:" >&2
    tail -n 50 "${GATEWAY_LOG_FILE}" >&2 || true
  fi
}

echo "[smoke] starting temporary postgres container ${PG_CONTAINER_NAME} on ${PG_PORT}"
docker run -d --rm \
  --name "${PG_CONTAINER_NAME}" \
  -e POSTGRES_USER="${PG_USER}" \
  -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
  -e POSTGRES_DB="${PG_DB}" \
  -p "127.0.0.1:${PG_PORT}:5432" \
  postgres:16-alpine >/dev/null

echo "[smoke] waiting for postgres"
for _ in $(seq 1 30); do
  if docker exec "${PG_CONTAINER_NAME}" pg_isready -U "${PG_USER}" -d "${PG_DB}" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${PG_CONTAINER_NAME}" pg_isready -U "${PG_USER}" -d "${PG_DB}" >/dev/null

echo "[smoke] running migrations"
(
  cd "${ROOT_DIR}"
  export POSTGRES_DSN
  env GOPATH=/tmp/go GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go/pkg/mod \
    go run ./cmd/banana-async-migrate up
)

echo "[smoke] starting async gateway at ${GATEWAY_ADDR}"
(
  cd "${ROOT_DIR}"
  export LISTEN_ADDR="${GATEWAY_ADDR}"
  export NEWAPI_BASE_URL
  export POSTGRES_DSN
  export OWNER_HASH_SECRET
  export TASK_PAYLOAD_ENCRYPTION_KEY
  export MAX_INFLIGHT_TASKS="${MAX_INFLIGHT_TASKS:-2}"
  export MAX_QUEUE_SIZE="${MAX_QUEUE_SIZE:-16}"
  export TASK_POLL_RETRY_AFTER_SEC="${TASK_POLL_RETRY_AFTER_SEC:-3}"
  env GOPATH=/tmp/go GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go/pkg/mod \
    go run ./cmd/banana-async-gateway >"${GATEWAY_LOG_FILE}" 2>&1
) &
GATEWAY_PID=$!

echo "[smoke] waiting for async gateway readiness"
readiness_exit_code=0
wait_for_gateway_ready "${GATEWAY_PID}" "${GATEWAY_URL}" 30 1 || readiness_exit_code=$?
if [[ "${readiness_exit_code}" -ne 0 ]]; then
  case "${readiness_exit_code}" in
    10)
      echo "[smoke] async gateway exited before readiness probe succeeded" >&2
      ;;
    11)
      echo "[smoke] async gateway readiness probe timed out" >&2
      ;;
    12)
      echo "[smoke] async gateway exited after readiness probe reported success" >&2
      ;;
    *)
      echo "[smoke] async gateway readiness failed with code ${readiness_exit_code}" >&2
      ;;
  esac
  print_gateway_log_tail
  exit 1
fi

echo "[smoke] running live async smoke test"
(
  cd "${ROOT_DIR}"
  export SMOKE_GATEWAY_BASE_URL="${GATEWAY_URL}"
  export SMOKE_API_KEY
  export SMOKE_MODEL="${SMOKE_MODEL:-gemini-3-pro-image-preview}"
  export SMOKE_PROMPT="${SMOKE_PROMPT:-draw a single ripe yellow banana on a clean white background}"
  export SMOKE_BODY_FILE="${SMOKE_BODY_FILE:-}"
  export SMOKE_TIMEOUT_SEC="${SMOKE_TIMEOUT_SEC:-600}"
  export SMOKE_POLL_INTERVAL_SEC="${SMOKE_POLL_INTERVAL_SEC:-3}"
  env GOPATH=/tmp/go GOCACHE=/tmp/go-build-cache GOMODCACHE=/tmp/go/pkg/mod \
    go run ./cmd/banana-async-smoke
)

echo "[smoke] success"
