#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TARGET_SCRIPT="${ROOT_DIR}/run_xinbao_async_pressure_test.sh"

test_fetch_content_does_not_treat_302_redirect_as_curl_failure() {
  if grep -q -- '-L --max-redirs 0' "${TARGET_SCRIPT}"; then
    echo "assert failed: fetch_content should not use '-L --max-redirs 0' because 302 is a healthy /content response" >&2
    exit 1
  fi
}

test_fetch_content_does_not_treat_302_redirect_as_curl_failure
