use std::sync::Arc;

use anyhow::{Result, anyhow};
use axum::body::{Body, to_bytes};
use axum::extract::{Path, Query, Request, State};
use axum::http::header::{
    ACCEPT, ACCESS_CONTROL_ALLOW_ORIGIN, AUTHORIZATION, CONTENT_TYPE,
};
use axum::http::{HeaderValue, StatusCode};
use axum::response::{IntoResponse, Response};
use axum::routing::{get, post};
use axum::{Json, Router};
use base64::Engine;
use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use serde::Deserialize;
use serde_json::{Value, json};
use url::{Url, form_urlencoded};

use crate::config::Config;
use crate::proxy_image::{hostname_matches_domain_patterns, is_forbidden_fetch_target};
use crate::request_rewrite::{RewriteServices, rewrite_request_inline_data};
use crate::response_rewrite::{normalize_gemini_response, rewrite_inline_data_base64_to_urls};
use crate::upload::Uploader;
use crate::upstream::{ResolvedUpstream, resolve_upstream_from_header_map};

const MAX_REQUEST_BODY_BYTES: usize = 20 * 1024 * 1024;

#[derive(Clone)]
struct AppState {
    config: Arc<Config>,
    upstream_client: reqwest::Client,
    image_client: reqwest::Client,
    uploader: Arc<Uploader>,
}

#[derive(Debug, Deserialize)]
struct ProxyImageQuery {
    url: Option<String>,
    u: Option<String>,
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum OutputMode {
    Base64,
    Url,
}

pub fn build_router(config: Config) -> Router {
    let upload_client = reqwest::Client::builder()
        .timeout(std::time::Duration::from_millis(config.upload_timeout_ms))
        .build()
        .unwrap_or_else(|_| reqwest::Client::new());
    let state = AppState {
        uploader: Arc::new(Uploader::new(upload_client, config.clone())),
        config: Arc::new(config),
        upstream_client: reqwest::Client::new(),
        image_client: reqwest::Client::new(),
    };

    Router::new()
        .route("/proxy/image", get(proxy_image))
        .route("/v1beta/models/{*rest}", post(model_action))
        .with_state(state)
}

async fn model_action(
    State(state): State<AppState>,
    Path(rest): Path<String>,
    request: Request,
) -> Response {
    let resolved = match resolve_upstream_from_header_map(
        request.headers(),
        &state.config.upstream_base_url,
        &state.config.upstream_api_key,
    ) {
        Ok(resolved) => resolved,
        Err(err) => {
            return (
                StatusCode::UNAUTHORIZED,
                Json(json!({"error": {"code": 401, "message": err.to_string()}})),
            )
                .into_response();
        }
    };

    let target_path = format!("/v1beta/models/{rest}");
    let is_stream = if rest.ends_with(":streamGenerateContent") {
        true
    } else if rest.ends_with(":generateContent") {
        false
    } else {
        return (
            StatusCode::NOT_FOUND,
            Json(json!({"error": {"code": 404, "message": "Not Found"}})),
        )
            .into_response();
    };

    match forward_gemini_request(state, resolved, target_path, request, is_stream).await {
        Ok(response) => response,
        Err(err) => (
            StatusCode::BAD_GATEWAY,
            Json(json!({"error": {"code": 502, "message": err.to_string()}})),
        )
            .into_response(),
    }
}

async fn forward_gemini_request(
    state: AppState,
    resolved: ResolvedUpstream,
    target_path: String,
    request: Request,
    is_stream: bool,
) -> Result<Response> {
    let request_headers = request.headers().clone();
    let request_query = request.uri().query().map(ToOwned::to_owned);
    let request_body = to_bytes(request.into_body(), MAX_REQUEST_BODY_BYTES)
        .await
        .map_err(|err| anyhow!("failed to read request body: {err}"))?;

    let mut body: Value = serde_json::from_slice(&request_body)
        .map_err(|err| anyhow!("invalid json body: {err}"))?;
    let output_mode = get_output_mode(request_query.as_deref(), &body);
    strip_output_from_value(&mut body);

    body = rewrite_request_inline_data(
        body,
        &RewriteServices {
            image_client: state.image_client.clone(),
            max_image_bytes: crate::image_io::DEFAULT_MAX_IMAGE_BYTES,
            allow_private_networks: false,
        },
    )
    .await?;

    let upstream_body = serde_json::to_vec(&body)?;
    let upstream_url = build_upstream_url(
        &resolved.base_url,
        &target_path,
        request_query.as_deref(),
    )?;

    let mut upstream_request = state.upstream_client.post(upstream_url).body(upstream_body);
    if let Some(value) = request_headers.get(CONTENT_TYPE) {
        upstream_request = upstream_request.header(CONTENT_TYPE, value.clone());
    }
    if let Some(value) = request_headers.get(ACCEPT) {
        upstream_request = upstream_request.header(ACCEPT, value.clone());
    }
    upstream_request = upstream_request.header("x-goog-api-key", resolved.api_key.clone());
    upstream_request = upstream_request.header(
        AUTHORIZATION,
        format!("Bearer {}", resolved.api_key),
    );

    let upstream_response = upstream_request.send().await?;

    if is_stream {
        handle_stream_response(
            upstream_response,
            output_mode,
            state.uploader.as_ref(),
            state.config.as_ref(),
        )
        .await
    } else {
        handle_non_stream_response(upstream_response, output_mode, state.uploader.as_ref(), state.config.as_ref()).await
    }
}

async fn handle_non_stream_response(
    upstream_response: reqwest::Response,
    output_mode: OutputMode,
    uploader: &Uploader,
    config: &Config,
) -> Result<Response> {
    let status = upstream_response.status();
    let content_type = upstream_response
        .headers()
        .get(CONTENT_TYPE)
        .cloned()
        .unwrap_or_else(|| HeaderValue::from_static("application/json"));
    let body_bytes = upstream_response.bytes().await?;

    if !status.is_success() {
        let mut response = Response::new(Body::from(body_bytes));
        *response.status_mut() = StatusCode::from_u16(status.as_u16())?;
        response.headers_mut().insert(CONTENT_TYPE, content_type);
        return Ok(response);
    }

    let json_body: Value = match serde_json::from_slice(&body_bytes) {
        Ok(body) => body,
        Err(_) => {
            let mut response = Response::new(Body::from(body_bytes));
            *response.status_mut() = StatusCode::from_u16(status.as_u16())?;
            response.headers_mut().insert(CONTENT_TYPE, content_type);
            return Ok(response);
        }
    };

    let mut final_json = normalize_gemini_response(json_body);
    if output_mode == OutputMode::Url {
        final_json = rewrite_inline_data_base64_to_urls(
            final_json,
            uploader,
            &config.public_base_url,
            config.proxy_standard_output_urls,
        )
        .await;
    }
    let final_body = serde_json::to_vec(&final_json)?;
    let mut response = Response::new(Body::from(final_body));
    *response.status_mut() = StatusCode::from_u16(status.as_u16())?;
    response.headers_mut().insert(CONTENT_TYPE, content_type);
    Ok(response)
}

async fn handle_stream_response(
    upstream_response: reqwest::Response,
    output_mode: OutputMode,
    uploader: &Uploader,
    config: &Config,
) -> Result<Response> {
    let status = upstream_response.status();
    let body_text = upstream_response.text().await?;

    let mut response = if !status.is_success() {
        Response::new(Body::from(body_text))
    } else {
        let rewritten =
            rewrite_stream_text(&body_text, output_mode, uploader, config).await?;
        let mut response = Response::new(Body::from(rewritten));
        response.headers_mut().insert(
            CONTENT_TYPE,
            HeaderValue::from_static("text/event-stream"),
        );
        response
    };

    *response.status_mut() = StatusCode::from_u16(status.as_u16())?;
    if !status.is_success() {
        response.headers_mut().insert(
            CONTENT_TYPE,
            HeaderValue::from_static("text/event-stream"),
        );
    }
    Ok(response)
}

async fn rewrite_stream_text(
    input: &str,
    output_mode: OutputMode,
    uploader: &Uploader,
    config: &Config,
) -> Result<String> {
    let mut output = String::with_capacity(input.len());

    for line in input.split_inclusive('\n') {
        let trimmed = line.trim_end_matches('\n').trim_end_matches('\r');
        let line_ending = &line[trimmed.len()..];

        if !trimmed.starts_with("data:") {
            output.push_str(trimmed);
            output.push_str(line_ending);
            continue;
        }

        let raw = trimmed.trim_start_matches("data:").trim();
        if raw.is_empty() || raw == "[DONE]" {
            output.push_str(trimmed);
            output.push_str(line_ending);
            continue;
        }

        match serde_json::from_str::<Value>(raw) {
            Ok(value) => {
                let mut rewritten = normalize_gemini_response(value);
                if output_mode == OutputMode::Url {
                    rewritten = rewrite_inline_data_base64_to_urls(
                        rewritten,
                        uploader,
                        &config.public_base_url,
                        config.proxy_standard_output_urls,
                    )
                    .await;
                }
                output.push_str("data: ");
                output.push_str(&serde_json::to_string(&rewritten)?);
                output.push_str(line_ending);
            }
            Err(_) => {
                output.push_str(trimmed);
                output.push_str(line_ending);
            }
        }
    }

    Ok(output)
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
        Ok(url)
            if matches!(url.scheme(), "http" | "https")
                && !url.host_str().unwrap_or("").is_empty() =>
        {
            url
        }
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

    match state.image_client.get(parsed).send().await {
        Ok(upstream_response) => {
            let status = upstream_response.status();
            let content_type = upstream_response
                .headers()
                .get(CONTENT_TYPE)
                .cloned()
                .unwrap_or_else(|| HeaderValue::from_static("application/octet-stream"));
            match upstream_response.bytes().await {
                Ok(body_bytes) => {
                    let mut response = Response::new(Body::from(body_bytes));
                    *response.status_mut() = StatusCode::from_u16(status.as_u16())
                        .unwrap_or(StatusCode::BAD_GATEWAY);
                    response
                        .headers_mut()
                        .insert(ACCESS_CONTROL_ALLOW_ORIGIN, HeaderValue::from_static("*"));
                    response.headers_mut().insert(CONTENT_TYPE, content_type);
                    response
                }
                Err(_) => (StatusCode::BAD_GATEWAY, "Proxy fetch failed").into_response(),
            }
        }
        Err(_) => (StatusCode::BAD_GATEWAY, "Proxy fetch failed").into_response(),
    }
}

fn get_output_mode(query: Option<&str>, body: &Value) -> OutputMode {
    if query_contains_output_url(query) {
        return OutputMode::Url;
    }

    if body
        .get("output")
        .and_then(Value::as_str)
        .is_some_and(|value| value.trim().eq_ignore_ascii_case("url"))
    {
        return OutputMode::Url;
    }

    if body
        .pointer("/generationConfig/imageConfig/output")
        .and_then(Value::as_str)
        .is_some_and(|value| value.trim().eq_ignore_ascii_case("url"))
    {
        return OutputMode::Url;
    }

    if body
        .pointer("/generation_config/image_config/output")
        .and_then(Value::as_str)
        .is_some_and(|value| value.trim().eq_ignore_ascii_case("url"))
    {
        return OutputMode::Url;
    }

    OutputMode::Base64
}

fn strip_output_from_value(body: &mut Value) {
    if let Some(map) = body.as_object_mut() {
        map.remove("output");
    }

    if let Some(image_config) = body.pointer_mut("/generationConfig/imageConfig") {
        if let Some(map) = image_config.as_object_mut() {
            map.remove("output");
        }
    }

    if let Some(image_config) = body.pointer_mut("/generation_config/image_config") {
        if let Some(map) = image_config.as_object_mut() {
            map.remove("output");
        }
    }
}

fn build_upstream_url(
    base_url: &str,
    path: &str,
    query: Option<&str>,
) -> Result<String> {
    let mut parsed = Url::parse(base_url)?;
    parsed.set_path(path);

    let filtered_query = filter_query_without_output(query);
    parsed.set_query(filtered_query.as_deref());
    Ok(parsed.to_string())
}

fn filter_query_without_output(query: Option<&str>) -> Option<String> {
    let query = query?;
    let mut serializer = form_urlencoded::Serializer::new(String::new());
    let mut has_any = false;
    for (key, value) in form_urlencoded::parse(query.as_bytes()) {
        if key == "output" {
            continue;
        }
        serializer.append_pair(key.as_ref(), value.as_ref());
        has_any = true;
    }
    if has_any {
        Some(serializer.finish())
    } else {
        None
    }
}

fn query_contains_output_url(query: Option<&str>) -> bool {
    query
        .into_iter()
        .flat_map(|query| form_urlencoded::parse(query.as_bytes()))
        .any(|(key, value)| key == "output" && value.trim().eq_ignore_ascii_case("url"))
}
