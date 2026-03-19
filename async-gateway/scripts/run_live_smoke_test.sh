#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${ROOT_DIR}/run_live_smoke_lib.sh"

assert_exit_code() {
  local actual="$1"
  local expected="$2"
  local message="$3"
  if [[ "${actual}" != "${expected}" ]]; then
    echo "assert failed: ${message}: got ${actual}, want ${expected}" >&2
    exit 1
  fi
}

test_wait_for_gateway_ready_fails_when_process_is_dead() {
  set +e
  wait_for_gateway_ready 999999 "http://127.0.0.1:9" 1 0
  local exit_code=$?
  set -e

  assert_exit_code "${exit_code}" "10" "dead process should fail before readiness probe"
}

test_wait_for_gateway_ready_fails_when_probe_never_succeeds() {
  sleep 10 &
  local pid=$!
  trap 'kill "${pid}" 2>/dev/null || true' RETURN

  set +e
  wait_for_gateway_ready "${pid}" "http://127.0.0.1:9" 1 0
  local exit_code=$?
  set -e

  assert_exit_code "${exit_code}" "11" "probe timeout should fail readiness"
  kill "${pid}" 2>/dev/null || true
  wait "${pid}" 2>/dev/null || true
  trap - RETURN
}

test_wait_for_gateway_ready_rejects_wrong_http_server() {
  local server_pid=""
  local sleep_pid=""
  local port
  port="$(python3 - <<'PY'
import socket
s = socket.socket()
s.bind(("127.0.0.1", 0))
print(s.getsockname()[1])
s.close()
PY
)"

  python3 -m http.server "${port}" --bind 127.0.0.1 >/dev/null 2>&1 &
  server_pid=$!
  sleep 10 &
  sleep_pid=$!
  trap 'kill "${server_pid}" "${sleep_pid}" 2>/dev/null || true' RETURN

  for _ in $(seq 1 20); do
    if curl -sS -o /dev/null "http://127.0.0.1:${port}/" 2>/dev/null; then
      break
    fi
    sleep 0.1
  done

  set +e
  wait_for_gateway_ready "${sleep_pid}" "http://127.0.0.1:${port}" 2 0
  local exit_code=$?
  set -e

  assert_exit_code "${exit_code}" "11" "non-matching server must not count as smoke gateway readiness"

  kill "${server_pid}" "${sleep_pid}" 2>/dev/null || true
  wait "${server_pid}" 2>/dev/null || true
  wait "${sleep_pid}" 2>/dev/null || true
  trap - RETURN
}

test_run_live_smoke_script_uses_readiness_guard() {
  if ! grep -q 'source ".*run_live_smoke_lib.sh"' "${ROOT_DIR}/run_live_smoke.sh"; then
    echo "assert failed: run_live_smoke.sh should source readiness helper" >&2
    exit 1
  fi
  if ! grep -q 'wait_for_gateway_ready "\${GATEWAY_PID}" "\${GATEWAY_URL}" 30 1 || readiness_exit_code=\$?' "${ROOT_DIR}/run_live_smoke.sh"; then
    echo "assert failed: run_live_smoke.sh should call wait_for_gateway_ready before smoke run" >&2
    exit 1
  fi
}

test_wait_for_gateway_ready_fails_when_process_is_dead
test_wait_for_gateway_ready_fails_when_probe_never_succeeds
test_wait_for_gateway_ready_rejects_wrong_http_server
test_run_live_smoke_script_uses_readiness_guard
