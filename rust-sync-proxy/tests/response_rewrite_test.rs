use serde_json::json;

#[test]
fn keeps_only_largest_inline_image_per_candidate() {
    let input = json!({
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
    assert_eq!(parts[0]["inlineData"]["data"], "aaaaaaaa");
}
