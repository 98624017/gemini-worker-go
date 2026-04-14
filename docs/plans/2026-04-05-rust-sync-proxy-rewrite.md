# Rust Sync Proxy Rewrite Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a standalone Rust implementation of the root synchronous Gemini proxy in a new `rust-sync-proxy/` directory without modifying the existing Go proxy code.

**Architecture:** Create a parallel Rust service with clear modules for routing, config, request rewrite, response rewrite, stream rewrite, image I/O, cache, and admin observability. Keep the current Go implementation untouched and use it as the behavioral baseline for fixture tests and rollout comparison.

**Tech Stack:** Rust stable, `axum`, `tokio`, `serde`, `serde_json`, `reqwest`, `tracing`, `bytes`, `base64`, `tokio-stream`, `mime`, `tempfile`

---

### Task 1: Bootstrap the standalone Rust crate

**Files:**
- Create: `rust-sync-proxy/Cargo.toml`
- Create: `rust-sync-proxy/src/main.rs`
- Create: `rust-sync-proxy/src/lib.rs`
- Create: `rust-sync-proxy/tests/http_smoke.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn unknown_route_returns_404() {
    let app = rust_sync_proxy::build_router(rust_sync_proxy::test_config());
    let response = app
        .oneshot(
            http::Request::builder()
                .uri("/not-found")
                .body(axum::body::Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), http::StatusCode::NOT_FOUND);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test unknown_route_returns_404 -- --nocapture`
Expected: FAIL because crate and router do not exist yet

**Step 3: Write minimal implementation**

```rust
pub fn build_router(_cfg: Config) -> axum::Router {
    axum::Router::new()
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test unknown_route_returns_404 -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/Cargo.toml rust-sync-proxy/src/main.rs rust-sync-proxy/src/lib.rs rust-sync-proxy/tests/http_smoke.rs
git commit -m "feat: bootstrap rust sync proxy crate"
```

### Task 2: Add config loading and startup validation

**Files:**
- Create: `rust-sync-proxy/src/config.rs`
- Modify: `rust-sync-proxy/src/lib.rs`
- Modify: `rust-sync-proxy/src/main.rs`
- Create: `rust-sync-proxy/tests/config_test.rs`

**Step 1: Write the failing test**

```rust
#[test]
fn defaults_match_go_proxy_expectations() {
    let cfg = rust_sync_proxy::config::Config::from_env_map(&std::collections::HashMap::new()).unwrap();
    assert_eq!(cfg.port, 8787);
    assert_eq!(cfg.upstream_base_url, "https://magic666.top");
    assert_eq!(cfg.image_host_mode.as_str(), "legacy");
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test defaults_match_go_proxy_expectations -- --nocapture`
Expected: FAIL because config module is missing

**Step 3: Write minimal implementation**

```rust
pub struct Config {
    pub port: u16,
    pub upstream_base_url: String,
    pub image_host_mode: String,
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test defaults_match_go_proxy_expectations -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/config.rs rust-sync-proxy/src/lib.rs rust-sync-proxy/src/main.rs rust-sync-proxy/tests/config_test.rs
git commit -m "feat: add rust proxy config parsing"
```

### Task 3: Build the top-level router and route guards

**Files:**
- Create: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/src/http/router.rs`
- Modify: `rust-sync-proxy/src/lib.rs`
- Create: `rust-sync-proxy/tests/router_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn generate_content_route_accepts_post_only() {
    let app = rust_sync_proxy::build_router(rust_sync_proxy::test_config());
    let response = app
        .oneshot(
            http::Request::builder()
                .method(http::Method::GET)
                .uri("/v1beta/models/demo:generateContent")
                .body(axum::body::Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), http::StatusCode::METHOD_NOT_ALLOWED);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test generate_content_route_accepts_post_only -- --nocapture`
Expected: FAIL because route handlers are not wired

**Step 3: Write minimal implementation**

```rust
Router::new()
    .route("/proxy/image", get(proxy_image))
    .route("/v1beta/models/:model:generateContent", post(generate_content).get(method_not_allowed))
    .route("/v1beta/models/:model:streamGenerateContent", post(stream_generate_content).get(method_not_allowed))
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test generate_content_route_accepts_post_only -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/http/mod.rs rust-sync-proxy/src/http/router.rs rust-sync-proxy/src/lib.rs rust-sync-proxy/tests/router_test.rs
git commit -m "feat: add top-level rust proxy routes"
```

### Task 4: Reproduce header parsing and upstream selection rules

**Files:**
- Create: `rust-sync-proxy/src/upstream.rs`
- Modify: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/tests/upstream_auth_test.rs`

**Step 1: Write the failing test**

```rust
#[test]
fn bearer_token_can_override_base_url_and_api_key() {
    let headers = [("authorization", "Bearer https://demo.example|secret")];
    let resolved = rust_sync_proxy::upstream::resolve_upstream(&headers, "https://magic666.top", "env-key").unwrap();
    assert_eq!(resolved.base_url, "https://demo.example");
    assert_eq!(resolved.api_key, "secret");
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test bearer_token_can_override_base_url_and_api_key -- --nocapture`
Expected: FAIL because upstream resolver is missing

**Step 3: Write minimal implementation**

```rust
pub struct ResolvedUpstream {
    pub base_url: String,
    pub api_key: String,
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test bearer_token_can_override_base_url_and_api_key -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/upstream.rs rust-sync-proxy/src/http/mod.rs rust-sync-proxy/tests/upstream_auth_test.rs
git commit -m "feat: add upstream auth override rules"
```

### Task 5: Implement `/proxy/image` allowlist and SSRF guard

**Files:**
- Create: `rust-sync-proxy/src/proxy_image.rs`
- Modify: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/tests/proxy_image_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn rejects_loopback_proxy_target() {
    let app = rust_sync_proxy::build_router(rust_sync_proxy::test_config());
    let response = app
        .oneshot(
            http::Request::builder()
                .uri("/proxy/image?url=http://127.0.0.1/test.png")
                .body(axum::body::Body::empty())
                .unwrap(),
        )
        .await
        .unwrap();
    assert_eq!(response.status(), http::StatusCode::FORBIDDEN);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test rejects_loopback_proxy_target -- --nocapture`
Expected: FAIL because proxy target validation is not implemented

**Step 3: Write minimal implementation**

```rust
pub fn is_allowed_proxy_target(url: &url::Url, allowed: &[String]) -> bool {
    // exact match and .suffix match
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test rejects_loopback_proxy_target -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/proxy_image.rs rust-sync-proxy/src/http/mod.rs rust-sync-proxy/tests/proxy_image_test.rs
git commit -m "feat: add proxy image target validation"
```

### Task 6: Add request JSON scanning for `inlineData` URL collection

**Files:**
- Create: `rust-sync-proxy/src/request_rewrite.rs`
- Create: `rust-sync-proxy/tests/request_rewrite_test.rs`

**Step 1: Write the failing test**

```rust
#[test]
fn collects_unique_inline_data_urls_and_enforces_limit() {
    let body = serde_json::json!({
        "contents": [{"parts": [{"inlineData": {"data": "https://img.example/a.png"}}]}]
    });
    let scan = rust_sync_proxy::request_rewrite::scan_inline_data_urls(&body).unwrap();
    assert_eq!(scan.unique_urls.len(), 1);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test collects_unique_inline_data_urls_and_enforces_limit -- --nocapture`
Expected: FAIL because request scanner is missing

**Step 3: Write minimal implementation**

```rust
pub struct InlineDataScan {
    pub unique_urls: Vec<String>,
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test collects_unique_inline_data_urls_and_enforces_limit -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/request_rewrite.rs rust-sync-proxy/tests/request_rewrite_test.rs
git commit -m "feat: add inline data request scanner"
```

### Task 7: Implement image download, size limits, MIME normalization, and cache interfaces

**Files:**
- Create: `rust-sync-proxy/src/image_io.rs`
- Create: `rust-sync-proxy/src/cache.rs`
- Create: `rust-sync-proxy/tests/image_io_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn rejects_images_over_max_size() {
    let result = rust_sync_proxy::image_io::enforce_max_size(10 * 1024 * 1024 + 1, 10 * 1024 * 1024);
    assert!(result.is_err());
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test rejects_images_over_max_size -- --nocapture`
Expected: FAIL because image I/O helpers do not exist

**Step 3: Write minimal implementation**

```rust
pub fn enforce_max_size(actual: usize, limit: usize) -> anyhow::Result<()> {
    if actual > limit { anyhow::bail!("image too large"); }
    Ok(())
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test rejects_images_over_max_size -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/image_io.rs rust-sync-proxy/src/cache.rs rust-sync-proxy/tests/image_io_test.rs
git commit -m "feat: add image io primitives and cache interfaces"
```

### Task 8: Implement request-side `inlineData URL -> base64` rewrite

**Files:**
- Modify: `rust-sync-proxy/src/request_rewrite.rs`
- Modify: `rust-sync-proxy/src/image_io.rs`
- Modify: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/tests/request_inline_data_flow_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn request_inline_data_urls_are_rewritten_to_base64() {
    // fixture server returns a tiny png
    // assert rewritten JSON contains inlineData.mimeType and base64 data
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test request_inline_data_urls_are_rewritten_to_base64 -- --nocapture`
Expected: FAIL because rewrite flow is incomplete

**Step 3: Write minimal implementation**

```rust
pub async fn rewrite_request_inline_data(
    body: serde_json::Value,
    services: &RewriteServices,
) -> anyhow::Result<serde_json::Value> {
    // scan -> fetch -> base64 -> patch json
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test request_inline_data_urls_are_rewritten_to_base64 -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/request_rewrite.rs rust-sync-proxy/src/image_io.rs rust-sync-proxy/src/http/mod.rs rust-sync-proxy/tests/request_inline_data_flow_test.rs
git commit -m "feat: rewrite request inline data urls to base64"
```

### Task 9: Implement non-stream response normalization and largest-image retention

**Files:**
- Create: `rust-sync-proxy/src/response_rewrite.rs`
- Create: `rust-sync-proxy/tests/response_rewrite_test.rs`

**Step 1: Write the failing test**

```rust
#[test]
fn keeps_only_largest_inline_image_per_candidate() {
    let input = serde_json::json!({
        "candidates": [{
            "content": {"parts": [
                {"inlineData": {"mimeType": "image/png", "data": "aaaa"}},
                {"inlineData": {"mimeType": "image/png", "data": "aaaaaaaa"}}
            ]}
        }]
    });
    let output = rust_sync_proxy::response_rewrite::keep_largest_inline_image(input);
    let parts = output["candidates"][0]["content"]["parts"].as_array().unwrap();
    assert_eq!(parts.len(), 1);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test keeps_only_largest_inline_image_per_candidate -- --nocapture`
Expected: FAIL because response rewrite module is missing

**Step 3: Write minimal implementation**

```rust
pub fn keep_largest_inline_image(body: serde_json::Value) -> serde_json::Value {
    body
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test keeps_only_largest_inline_image_per_candidate -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/response_rewrite.rs rust-sync-proxy/tests/response_rewrite_test.rs
git commit -m "feat: normalize non-stream image responses"
```

### Task 10: Implement `output=url` upload abstraction with legacy and R2 modes

**Files:**
- Create: `rust-sync-proxy/src/upload.rs`
- Modify: `rust-sync-proxy/src/response_rewrite.rs`
- Create: `rust-sync-proxy/tests/upload_mode_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn r2_then_legacy_falls_back_to_legacy_on_r2_failure() {
    let result = rust_sync_proxy::upload::upload_image_with_mode(
        rust_sync_proxy::upload::ImageHostMode::R2ThenLegacy,
        b"png-bytes",
        "image/png",
        &failing_r2(),
        &working_legacy(),
    )
    .await
    .unwrap();
    assert_eq!(result.provider, "legacy");
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test r2_then_legacy_falls_back_to_legacy_on_r2_failure -- --nocapture`
Expected: FAIL because uploader abstraction is missing

**Step 3: Write minimal implementation**

```rust
pub enum ImageHostMode {
    Legacy,
    R2,
    R2ThenLegacy,
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test r2_then_legacy_falls_back_to_legacy_on_r2_failure -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/upload.rs rust-sync-proxy/src/response_rewrite.rs rust-sync-proxy/tests/upload_mode_test.rs
git commit -m "feat: add upload mode abstraction"
```

### Task 11: Implement streaming SSE rewrite pipeline

**Files:**
- Create: `rust-sync-proxy/src/stream_rewrite.rs`
- Modify: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/tests/stream_rewrite_test.rs`

**Step 1: Write the failing test**

```rust
#[tokio::test]
async fn rewrites_sse_data_chunks_without_buffering_whole_stream() {
    // feed two SSE data events and assert transformed output preserves framing
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test rewrites_sse_data_chunks_without_buffering_whole_stream -- --nocapture`
Expected: FAIL because stream rewrite pipeline is missing

**Step 3: Write minimal implementation**

```rust
pub async fn rewrite_sse_stream(/* args */) -> impl futures::Stream<Item = anyhow::Result<bytes::Bytes>> {
    // parse event framing incrementally and rewrite only data payloads
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test rewrites_sse_data_chunks_without_buffering_whole_stream -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/stream_rewrite.rs rust-sync-proxy/src/http/mod.rs rust-sync-proxy/tests/stream_rewrite_test.rs
git commit -m "feat: add sse rewrite pipeline"
```

### Task 12: Add admin log sanitization and lightweight metrics

**Files:**
- Create: `rust-sync-proxy/src/admin.rs`
- Modify: `rust-sync-proxy/src/http/mod.rs`
- Create: `rust-sync-proxy/tests/admin_test.rs`

**Step 1: Write the failing test**

```rust
#[test]
fn admin_log_omits_base64_payloads() {
    let sanitized = rust_sync_proxy::admin::sanitize_json_for_log(
        br#"{"inlineData":{"data":"QUJDREVGRw=="}}"#
    );
    assert!(std::str::from_utf8(&sanitized).unwrap().contains("[base64 omitted"));
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test admin_log_omits_base64_payloads -- --nocapture`
Expected: FAIL because admin sanitization is missing

**Step 3: Write minimal implementation**

```rust
pub fn sanitize_json_for_log(input: &[u8]) -> Vec<u8> {
    input.to_vec()
}
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test admin_log_omits_base64_payloads -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/src/admin.rs rust-sync-proxy/src/http/mod.rs rust-sync-proxy/tests/admin_test.rs
git commit -m "feat: add admin log sanitization"
```

### Task 13: Build Go/Rust fixture comparison harness

**Files:**
- Create: `rust-sync-proxy/tests/fixtures/request_inline_data.json`
- Create: `rust-sync-proxy/tests/fixtures/response_multi_image.json`
- Create: `rust-sync-proxy/tests/fixtures/stream_sample.txt`
- Create: `rust-sync-proxy/tests/go_compat_test.rs`
- Create: `rust-sync-proxy/scripts/compare_with_go.sh`

**Step 1: Write the failing test**

```rust
#[test]
fn fixture_outputs_match_documented_go_behavior() {
    let fixture = include_str!("fixtures/response_multi_image.json");
    let output = rust_sync_proxy::response_rewrite::keep_largest_inline_image(
        serde_json::from_str(fixture).unwrap(),
    );
    assert_eq!(output["candidates"][0]["content"]["parts"].as_array().unwrap().len(), 1);
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test fixture_outputs_match_documented_go_behavior -- --nocapture`
Expected: FAIL until fixtures and compatibility harness are in place

**Step 3: Write minimal implementation**

```bash
#!/usr/bin/env bash
set -euo pipefail
echo "TODO: start go proxy, start rust proxy, replay fixtures, compare outputs"
exit 1
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test fixture_outputs_match_documented_go_behavior -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/tests/fixtures rust-sync-proxy/tests/go_compat_test.rs rust-sync-proxy/scripts/compare_with_go.sh
git commit -m "test: add go rust compatibility fixtures"
```

### Task 14: Add benchmark and rollout documentation

**Files:**
- Create: `rust-sync-proxy/README.md`
- Create: `rust-sync-proxy/scripts/run_local_benchmark.sh`
- Create: `docs/plans/2026-04-05-rust-sync-proxy-rollout-checklist.md`

**Step 1: Write the failing test**

```rust
#[test]
fn benchmark_script_mentions_rss_and_p95() {
    let script = std::fs::read_to_string("scripts/run_local_benchmark.sh").unwrap();
    assert!(script.contains("RSS"));
    assert!(script.contains("P95"));
}
```

**Step 2: Run test to verify it fails**

Run: `cd rust-sync-proxy && cargo test benchmark_script_mentions_rss_and_p95 -- --nocapture`
Expected: FAIL because docs and scripts do not exist

**Step 3: Write minimal implementation**

```bash
#!/usr/bin/env bash
set -euo pipefail
echo "Measure RSS, P95, P99, OOM count, and safe concurrency"
```

**Step 4: Run test to verify it passes**

Run: `cd rust-sync-proxy && cargo test benchmark_script_mentions_rss_and_p95 -- --nocapture`
Expected: PASS

**Step 5: Commit**

```bash
git add rust-sync-proxy/README.md rust-sync-proxy/scripts/run_local_benchmark.sh docs/plans/2026-04-05-rust-sync-proxy-rollout-checklist.md
git commit -m "docs: add rust proxy rollout checklist"
```

### Task 15: Run the full verification suite

**Files:**
- Modify: `rust-sync-proxy/README.md`

**Step 1: Run focused unit tests**

Run: `cd rust-sync-proxy && cargo test request_inline_data_urls_are_rewritten_to_base64 keeps_only_largest_inline_image_per_candidate r2_then_legacy_falls_back_to_legacy_on_r2_failure -- --nocapture`
Expected: PASS

**Step 2: Run full Rust test suite**

Run: `cd rust-sync-proxy && cargo test -- --nocapture`
Expected: PASS

**Step 3: Run formatting and lint**

Run: `cd rust-sync-proxy && cargo fmt --all -- --check && cargo clippy --all-targets --all-features -- -D warnings`
Expected: PASS

**Step 4: Run Go/Rust compatibility script**

Run: `cd rust-sync-proxy && bash ./scripts/compare_with_go.sh`
Expected: PASS with no output diffs on current fixture set

**Step 5: Commit**

```bash
git add rust-sync-proxy/README.md
git commit -m "chore: verify rust sync proxy rewrite baseline"
```

