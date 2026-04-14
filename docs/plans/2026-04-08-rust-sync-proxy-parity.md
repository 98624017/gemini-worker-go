# Rust Sync Proxy Parity Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Bring `rust-sync-proxy` to feature parity with the Go sync proxy for the currently missing admin, markdown-image normalization, and request-side cache/bridge features.

**Architecture:** Extend the existing Rust module layout instead of collapsing logic into the router. `admin.rs` owns observability data structures and auth, `response_rewrite.rs` owns markdown-image normalization, and `cache.rs` plus `request_rewrite.rs` own request-side fetch reuse, disk cache, and background bridge behavior.

**Tech Stack:** Rust, axum, tokio, serde_json, reqwest, std fs, existing test suite

---

### Task 1: Add failing tests for admin routes and markdown-image normalization

**Files:**
- Modify: `rust-sync-proxy/tests/admin_test.rs`
- Modify: `rust-sync-proxy/tests/http_forwarding_test.rs`

**Step 1: Write the failing tests**

- Add an admin route test that expects `/admin/api/logs` to require Basic Auth and return `200` with valid credentials.
- Add a forwarding test that expects markdown image text from upstream to be normalized into `inlineData`.

**Step 2: Run tests to verify they fail**

Run: `cd rust-sync-proxy && cargo test admin_routes -- --nocapture`
Expected: FAIL because router does not expose admin routes

Run: `cd rust-sync-proxy && cargo test markdown_image -- --nocapture`
Expected: FAIL because markdown normalization is not implemented

**Step 3: Write minimal implementation**

- Add config fields and router wiring for admin routes
- Add markdown image normalization in response rewrite path

**Step 4: Run tests to verify they pass**

Run the two focused tests again and confirm PASS.

### Task 2: Implement admin route state, auth, logs, and stats

**Files:**
- Modify: `rust-sync-proxy/src/admin.rs`
- Modify: `rust-sync-proxy/src/config.rs`
- Modify: `rust-sync-proxy/src/http/router.rs`
- Modify: `rust-sync-proxy/src/lib.rs`

**Step 1: Write the failing test**

- Add a test that makes a model request, then reads `/admin/api/stats` and expects `totalRequests` to increase.

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test admin_stats -- --nocapture`
Expected: FAIL because stats are not tracked

**Step 3: Write minimal implementation**

- Introduce admin shared state with ring buffer and counters
- Record request/response summaries in `model_action`
- Expose `/admin`, `/admin/logs`, `/admin/api/logs`, `/admin/api/stats`

**Step 4: Run tests to verify they pass**

Run: `cd rust-sync-proxy && cargo test admin_ -- --nocapture`
Expected: PASS

### Task 3: Implement request-side cache and background fetch bridge

**Files:**
- Modify: `rust-sync-proxy/src/cache.rs`
- Modify: `rust-sync-proxy/src/config.rs`
- Modify: `rust-sync-proxy/src/request_rewrite.rs`
- Modify: `rust-sync-proxy/src/http/router.rs`
- Add: `rust-sync-proxy/tests/request_cache_test.rs`

**Step 1: Write the failing tests**

- Add a cache-hit test that reuses one fetched URL across repeated requests
- Add a bridge test that keeps one slow fetch in flight and lets a later retry reuse it

**Step 2: Run tests to verify they fail**

Run: `cd rust-sync-proxy && cargo test request_cache -- --nocapture`
Expected: FAIL because current rewrite path always fetches directly

**Step 3: Write minimal implementation**

- Add memory cache, disk cache metadata, inflight dedupe, and background bridge
- Plumb config values through router state into request rewrite

**Step 4: Run tests to verify they pass**

Run: `cd rust-sync-proxy && cargo test request_cache -- --nocapture`
Expected: PASS

### Task 4: Expand config compatibility and end-to-end verification

**Files:**
- Modify: `rust-sync-proxy/tests/config_test.rs`
- Modify: `rust-sync-proxy/README.md`
- Modify: `rust-sync-proxy/scripts/compare_with_go.sh`

**Step 1: Write the failing tests**

- Extend config tests to assert new Go-compatible env defaults
- Extend the compare script to cover admin and markdown normalization

**Step 2: Run tests to verify they fail**

Run: `cd rust-sync-proxy && cargo test defaults_match_go_proxy_expectations -- --nocapture`
Expected: FAIL until new config fields are added

**Step 3: Write minimal implementation**

- Add the missing config parsing and docs
- Extend the comparison harness

**Step 4: Run full verification**

Run:

```bash
cd rust-sync-proxy
timeout 60s ~/.cargo/bin/cargo test --tests -- --nocapture
timeout 60s env GO_IMPL_ROOT=/home/feng/project/banana-proxy/geminiworker/go-implementation bash ./scripts/compare_with_go.sh
```

Expected: PASS
