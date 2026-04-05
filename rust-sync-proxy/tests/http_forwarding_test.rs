use std::sync::Arc;

use axum::body::{Body, Bytes, to_bytes};
use axum::extract::State;
use axum::http::header::CONTENT_TYPE;
use axum::http::{HeaderMap, HeaderValue, Request, StatusCode, Uri};
use axum::response::IntoResponse;
use axum::routing::post;
use axum::{Json, Router};
use serde_json::{Value, json};
use tokio::net::TcpListener;
use tokio::sync::Mutex;
use tower::ServiceExt;

#[derive(Clone, Default)]
struct UpstreamCapture {
    request_body: Arc<Mutex<Vec<u8>>>,
    query_string: Arc<Mutex<String>>,
    api_key: Arc<Mutex<String>>,
    authorization: Arc<Mutex<String>>,
}

#[tokio::test]
async fn generate_content_forwards_rewritten_request_and_normalizes_response() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:generateContent",
            post(mock_generate_content),
        )
        .with_state(capture.clone());

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let upstream_addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, upstream).await.unwrap();
    });

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{}", upstream_addr);
    config.upstream_api_key = "env-key".to_string();

    let app = rust_sync_proxy::build_router(config);
    let request_body = json!({
        "output": "url",
        "contents": [{"parts": [{"text": "hello"}]}]
    });

    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1beta/models/demo:generateContent?lang=zh")
                .header(CONTENT_TYPE, "application/json")
                .body(Body::from(request_body.to_string()))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let json_body: Value = serde_json::from_slice(&body).unwrap();

    assert!(json_body.get("thoughtSignature").is_none());
    let parts = json_body["candidates"][0]["content"]["parts"]
        .as_array()
        .unwrap();
    assert_eq!(parts.len(), 2);
    assert_eq!(parts[0]["text"], "kept");
    assert_eq!(parts[1]["inlineData"]["data"], "aaaaaaaa");

    let captured_body = String::from_utf8(capture.request_body.lock().await.clone()).unwrap();
    assert!(!captured_body.contains("\"output\""));
    assert_eq!(*capture.query_string.lock().await, "lang=zh");
    assert_eq!(*capture.api_key.lock().await, "env-key");
    assert_eq!(*capture.authorization.lock().await, "Bearer env-key");
}

#[tokio::test]
async fn stream_generate_content_forwards_and_rewrites_sse() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:streamGenerateContent",
            post(mock_stream_generate_content),
        )
        .with_state(capture.clone());

    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let upstream_addr = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, upstream).await.unwrap();
    });

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{}", upstream_addr);
    config.upstream_api_key = "env-key".to_string();

    let app = rust_sync_proxy::build_router(config);
    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1beta/models/demo:streamGenerateContent")
                .header(CONTENT_TYPE, "application/json")
                .body(Body::from(r#"{"contents":[{"parts":[{"text":"hello"}]}]}"#))
                .unwrap(),
        )
        .await
        .unwrap();

    assert_eq!(response.status(), StatusCode::OK);
    assert_eq!(
        response.headers().get(CONTENT_TYPE).unwrap(),
        "text/event-stream"
    );

    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let text = String::from_utf8(body.to_vec()).unwrap();
    assert!(text.contains("data: [DONE]"));
    assert!(text.contains("\"data\":\"bbbbbbbb\""));
    assert!(!text.contains("\"data\":\"aaaa\""));
    assert!(!text.contains("thoughtSignature"));
}

async fn mock_generate_content(
    State(capture): State<UpstreamCapture>,
    headers: HeaderMap,
    uri: Uri,
    body: Bytes,
) -> impl IntoResponse {
    store_request_capture(&capture, &headers, &uri).await;
    *capture.request_body.lock().await = body.to_vec();

    Json(json!({
        "thoughtSignature": "secret",
        "candidates": [{
            "finishReason": "STOP",
            "content": {
                "parts": [
                    { "inlineData": { "mimeType": "image/png", "data": "aaaa" } },
                    { "text": "kept" },
                    { "inlineData": { "mimeType": "image/png", "data": "aaaaaaaa" } }
                ]
            }
        }]
    }))
}

async fn mock_stream_generate_content(
    State(capture): State<UpstreamCapture>,
    headers: HeaderMap,
    uri: Uri,
    body: Bytes,
) -> impl IntoResponse {
    store_request_capture(&capture, &headers, &uri).await;
    *capture.request_body.lock().await = body.to_vec();

    let mut response_headers = HeaderMap::new();
    response_headers.insert(CONTENT_TYPE, HeaderValue::from_static("text/event-stream"));
    (
        response_headers,
        concat!(
            "event: message\n",
            "data: {\"thoughtSignature\":\"secret\",\"candidates\":[{\"content\":{\"parts\":[{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"aaaa\"}},{\"inlineData\":{\"mimeType\":\"image/png\",\"data\":\"bbbbbbbb\"}}]}}]}\n",
            "\n",
            "data: [DONE]\n"
        ),
    )
}

async fn store_request_capture(
    capture: &UpstreamCapture,
    headers: &HeaderMap,
    uri: &Uri,
) {
    *capture.query_string.lock().await = uri.query().unwrap_or_default().to_string();
    *capture.api_key.lock().await = headers
        .get("x-goog-api-key")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_string();
    *capture.authorization.lock().await = headers
        .get("authorization")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_string();
}
