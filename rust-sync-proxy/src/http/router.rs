use std::sync::Arc;

use axum::extract::{Path, Query, State};
use axum::http::StatusCode;
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use serde::Deserialize;
use serde_json::json;
use url::Url;

use crate::config::Config;
use crate::proxy_image::{hostname_matches_domain_patterns, is_forbidden_fetch_target};
use crate::upstream::resolve_upstream_from_header_map;

#[derive(Clone)]
struct AppState {
    config: Arc<Config>,
}

#[derive(Debug, Deserialize)]
struct ProxyImageQuery {
    url: Option<String>,
    u: Option<String>,
}

pub fn build_router(config: Config) -> Router {
    let state = AppState {
        config: Arc::new(config),
    };

    Router::new()
        .route("/proxy/image", get(proxy_image))
        .route("/v1beta/models/{*rest}", post(model_action))
        .with_state(state)
}

async fn model_action(
    State(state): State<AppState>,
    headers: axum::http::HeaderMap,
    Path(rest): Path<String>,
) -> Response {
    if let Err(err) = resolve_upstream_from_header_map(
        &headers,
        &state.config.upstream_base_url,
        &state.config.upstream_api_key,
    ) {
        return (
            StatusCode::UNAUTHORIZED,
            Json(json!({"error": {"code": 401, "message": err.to_string()}})),
        )
            .into_response();
    }

    if let Some(model) = rest.strip_suffix(":generateContent") {
        return generate_content(model).await;
    }
    if let Some(model) = rest.strip_suffix(":streamGenerateContent") {
        return stream_generate_content(model).await;
    }

    (
        StatusCode::NOT_FOUND,
        Json(json!({"error": {"code": 404, "message": "Not Found"}})),
    )
        .into_response()
}

async fn proxy_image(
    State(state): State<AppState>,
    Query(query): Query<ProxyImageQuery>,
) -> Response {
    let target = match query.url.as_deref().map(str::trim).filter(|v| !v.is_empty()) {
        Some(url) => url.to_string(),
        None => match query.u.as_deref().map(str::trim).filter(|v| !v.is_empty()) {
            Some(encoded) => match URL_SAFE_NO_PAD.decode(encoded) {
                Ok(bytes) => String::from_utf8(bytes).unwrap_or_default(),
                Err(_) => return (StatusCode::BAD_REQUEST, "Invalid u param").into_response(),
            },
            None => return (StatusCode::BAD_REQUEST, "Missing url param").into_response(),
        },
    };

    let parsed = match Url::parse(&target) {
        Ok(url) if matches!(url.scheme(), "http" | "https") && !url.host_str().unwrap_or("").is_empty() => url,
        _ => return (StatusCode::FORBIDDEN, "Forbidden proxy target").into_response(),
    };

    if is_forbidden_fetch_target(&parsed)
        || !hostname_matches_domain_patterns(
            parsed.host_str().unwrap_or_default(),
            &state.config.allowed_proxy_domains,
        )
    {
        return (StatusCode::FORBIDDEN, "Forbidden proxy target").into_response();
    }

    (
        StatusCode::NOT_IMPLEMENTED,
        "Proxy fetch is not implemented yet",
    )
        .into_response()
}

async fn generate_content(model: &str) -> Response {
    (
        StatusCode::NOT_IMPLEMENTED,
        Json(json!({"message": format!("generateContent is not implemented yet for {}", model)})),
    )
        .into_response()
}

async fn stream_generate_content(model: &str) -> Response {
    (
        StatusCode::NOT_IMPLEMENTED,
        Json(json!({"message": format!("streamGenerateContent is not implemented yet for {}", model)})),
    )
        .into_response()
}
