use axum::Router;
use axum::extract::State;
use axum::http::{HeaderMap, HeaderValue, StatusCode, header::CONTENT_TYPE};
use axum::routing::get;
use serde_json::json;
use tokio::net::TcpListener;

#[derive(Clone)]
struct ImageState {
    png: Vec<u8>,
}

#[tokio::test]
async fn request_inline_data_urls_are_rewritten_to_base64() {
    let (image_url, _server) = spawn_png_server().await;

    let body = json!({
        "contents": [{
            "parts": [{
                "inlineData": {
                    "data": image_url
                }
            }]
        }]
    });

    let rewritten = rust_sync_proxy::request_rewrite::rewrite_request_inline_data(
        body,
        &rust_sync_proxy::request_rewrite::RewriteServices {
            image_client: reqwest::Client::new(),
            max_image_bytes: rust_sync_proxy::image_io::DEFAULT_MAX_IMAGE_BYTES,
            allow_private_networks: true,
            fetch_service: None,
            cache_observer: None,
        },
    )
    .await
    .unwrap();

    let inline_data = &rewritten["contents"][0]["parts"][0]["inlineData"];
    assert_eq!(inline_data["mimeType"], "image/png");
    assert_eq!(inline_data["data"], "iVBORw0KGgo=");
}

#[tokio::test]
async fn request_inline_data_urls_are_materialized_to_blob_handles() {
    let (image_url, _server) = spawn_png_server().await;
    let runtime = rust_sync_proxy::test_blob_runtime(8 * 1024 * 1024);
    let body = json!({
        "contents": [{
            "parts": [{
                "inlineData": {
                    "data": image_url
                }
            }]
        }]
    });

    let materialized = rust_sync_proxy::request_materialize::materialize_request_images(
        body,
        &runtime,
        &reqwest::Client::new(),
    )
    .await
    .unwrap();

    assert_eq!(materialized.replacements.len(), 1);
    assert_eq!(materialized.replacements[0].mime_type, "image/png");
    assert!(runtime.is_inline(&materialized.replacements[0].blob).await);
}

async fn spawn_png_server() -> (String, tokio::task::JoinHandle<()>) {
    let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
    let address = listener.local_addr().unwrap();
    let state = ImageState {
        png: vec![137, 80, 78, 71, 13, 10, 26, 10],
    };

    let app = Router::new()
        .route("/image.png", get(serve_png))
        .with_state(state);

    let server = tokio::spawn(async move {
        axum::serve(listener, app).await.unwrap();
    });

    (format!("http://{address}/image.png"), server)
}

async fn serve_png(State(state): State<ImageState>) -> (StatusCode, HeaderMap, Vec<u8>) {
    let mut headers = HeaderMap::new();
    headers.insert(CONTENT_TYPE, HeaderValue::from_static("image/png"));
    (StatusCode::OK, headers, state.png)
}
