#[test]
fn admin_log_omits_base64_payloads() {
    let sanitized = rust_sync_proxy::admin::sanitize_json_for_log(
        br#"{"inlineData":{"data":"QUJDREVGRw=="}}"#,
    );
    assert!(sanitized.pretty.contains("[base64 omitted len=12]"));
}

#[test]
fn admin_log_collects_proxy_and_http_image_urls() {
    let sanitized = rust_sync_proxy::admin::sanitize_json_for_log(
        br#"{"parts":[{"inlineData":{"data":"https://img.example/a.png"}},{"inline_data":{"data":"/proxy/image?u=abc"}}]}"#,
    );
    assert_eq!(
        sanitized.image_urls,
        vec![
            "/proxy/image?u=abc".to_string(),
            "https://img.example/a.png".to_string()
        ]
    );
}

#[test]
fn extract_finish_reason_returns_first_candidate_reason() {
    let body: serde_json::Value = serde_json::from_str(
        r#"{"candidates":[{"finishReason":"STOP"},{"finishReason":"OTHER"}]}"#,
    )
    .unwrap();
    assert_eq!(
        rust_sync_proxy::admin::extract_finish_reason(&body).as_deref(),
        Some("STOP")
    );
}
