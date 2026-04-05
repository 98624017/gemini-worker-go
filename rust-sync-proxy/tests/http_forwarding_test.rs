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

#[derive(Clone, Default)]
struct UploadCapture {
    request_count: Arc<Mutex<usize>>,
    content_type: Arc<Mutex<String>>,
    user_agent: Arc<Mutex<String>>,
}

#[tokio::test]
async fn generate_content_forwards_rewritten_request_and_normalizes_response_in_base64_mode() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:generateContent",
            post(mock_generate_content),
        )
        .with_state(capture.clone());
    let upstream_addr = spawn_server(upstream).await;

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{}", upstream_addr);
    config.upstream_api_key = "env-key".to_string();

    let app = rust_sync_proxy::build_router(config);
    let request_body = json!({
        "output": "base64",
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
async fn generate_content_rewrites_inline_data_to_wrapped_urls_when_output_url_enabled() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:generateContent",
            post(mock_generate_content),
        )
        .with_state(capture.clone());
    let upstream_addr = spawn_server(upstream).await;

    let upload_capture = UploadCapture::default();
    let upload = Router::new()
        .route("/uguu", post(mock_legacy_upload))
        .with_state(upload_capture.clone());
    let upload_addr = spawn_server(upload).await;

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{}", upstream_addr);
    config.upstream_api_key = "env-key".to_string();
    config.public_base_url = "https://proxy.example.com".to_string();
    config.legacy_uguu_upload_url = format!("http://{upload_addr}/uguu");

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
    assert_eq!(
        parts[1]["inlineData"]["data"],
        "https://proxy.example.com/proxy/image?url=https%3A%2F%2Fh.uguu.se%2Ffixed-image.png"
    );

    let captured_body = String::from_utf8(capture.request_body.lock().await.clone()).unwrap();
    assert!(!captured_body.contains("\"output\""));
    assert_eq!(*capture.query_string.lock().await, "lang=zh");
    assert_eq!(*capture.api_key.lock().await, "env-key");
    assert_eq!(*capture.authorization.lock().await, "Bearer env-key");
    assert_eq!(*upload_capture.request_count.lock().await, 1);
    assert!(
        upload_capture
            .content_type
            .lock()
            .await
            .starts_with("multipart/form-data; boundary=")
    );
    assert_eq!(
        *upload_capture.user_agent.lock().await,
        "ComfyUI-Banana/1.0"
    );
}

#[tokio::test]
async fn stream_generate_content_forwards_and_rewrites_sse_in_base64_mode() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:streamGenerateContent",
            post(mock_stream_generate_content),
        )
        .with_state(capture.clone());
    let upstream_addr = spawn_server(upstream).await;

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

#[tokio::test]
async fn stream_generate_content_rewrites_inline_data_to_wrapped_urls_when_output_url_enabled() {
    let capture = UpstreamCapture::default();
    let upstream = Router::new()
        .route(
            "/v1beta/models/demo:streamGenerateContent",
            post(mock_stream_generate_content),
        )
        .with_state(capture.clone());
    let upstream_addr = spawn_server(upstream).await;

    let upload_capture = UploadCapture::default();
    let upload = Router::new()
        .route("/uguu", post(mock_legacy_upload))
        .with_state(upload_capture.clone());
    let upload_addr = spawn_server(upload).await;

    let mut config = rust_sync_proxy::test_config();
    config.upstream_base_url = format!("http://{}", upstream_addr);
    config.upstream_api_key = "env-key".to_string();
    config.public_base_url = "https://proxy.example.com".to_string();
    config.legacy_uguu_upload_url = format!("http://{upload_addr}/uguu");

    let app = rust_sync_proxy::build_router(config);
    let response = app
        .oneshot(
            Request::builder()
                .method("POST")
                .uri("/v1beta/models/demo:streamGenerateContent")
                .header(CONTENT_TYPE, "application/json")
                .body(Body::from(
                    r#"{"output":"url","contents":[{"parts":[{"text":"hello"}]}]}"#,
                ))
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
    assert!(text.contains(
        "\"data\":\"https://proxy.example.com/proxy/image?url=https%3A%2F%2Fh.uguu.se%2Ffixed-image.png\""
    ));
    assert!(!text.contains("\"data\":\"bbbbbbbb\""));
    assert!(!text.contains("\"data\":\"aaaa\""));
    assert!(!text.contains("thoughtSignature"));
    assert_eq!(*upload_capture.request_count.lock().await, 1);

    let captured_body = String::from_utf8(capture.request_body.lock().await.clone()).unwrap();
    assert!(!captured_body.contains("\"output\""));
    assert_eq!(*capture.api_key.lock().await, "env-key");
    assert_eq!(*capture.authorization.lock().await, "Bearer env-key");
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

async fn mock_legacy_upload(
    State(capture): State<UploadCapture>,
    headers: HeaderMap,
    body: Bytes,
) -> impl IntoResponse {
    *capture.request_count.lock().await += 1;
    *capture.content_type.lock().await = headers
        .get(CONTENT_TYPE)
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_string();
    *capture.user_agent.lock().await = headers
        .get("user-agent")
        .and_then(|value| value.to_str().ok())
        .unwrap_or_default()
        .to_string();
    assert!(!body.is_empty());

    Json(json!({
        "success": true,
        "files": [{
            "url": "https://h.uguu.se/fixed-image.png"
        }]
    }))
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

async fn spawn_server(app: Router) -> std::net::SocketAddr {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });
    address
}
