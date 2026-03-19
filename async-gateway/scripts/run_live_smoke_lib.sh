#!/usr/bin/env bash

wait_for_gateway_ready() {
  local gateway_pid="$1"
  local gateway_url="$2"
  local attempts="$3"
  local sleep_seconds="$4"

  local ready="0"
  local http_code=""

  for _ in $(seq 1 "${attempts}"); do
    if ! kill -0 "${gateway_pid}" 2>/dev/null; then
      return 10
    fi

    http_code="$(curl -sS -o /dev/null -w "%{http_code}" -H "Authorization: Bearer smoke-probe" "${gateway_url}/v1/tasks" 2>/dev/null || true)"
    if [[ "${http_code}" == "200" || "${http_code}" == "401" || "${http_code}" == "429" ]]; then
      ready="1"
      break
    fi

    sleep "${sleep_seconds}"
  done

  if [[ "${ready}" != "1" ]]; then
    return 11
  fi

  if ! kill -0 "${gateway_pid}" 2>/dev/null; then
    return 12
  fi

  return 0
}
