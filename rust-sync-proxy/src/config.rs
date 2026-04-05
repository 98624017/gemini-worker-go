use std::collections::HashMap;
use std::env;

use anyhow::{Result, anyhow};

const DEFAULT_UPSTREAM_BASE_URL: &str = "https://magic666.top";
const DEFAULT_PORT: u16 = 8787;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Config {
    pub port: u16,
    pub upstream_base_url: String,
    pub upstream_api_key: String,
    pub image_host_mode: String,
    pub allowed_proxy_domains: Vec<String>,
    pub public_base_url: String,
    pub proxy_standard_output_urls: bool,
    pub upload_timeout_ms: u64,
    pub legacy_uguu_upload_url: String,
    pub legacy_kefan_upload_url: String,
    pub r2_endpoint: String,
    pub r2_bucket: String,
    pub r2_access_key_id: String,
    pub r2_secret_access_key: String,
    pub r2_public_base_url: String,
    pub r2_object_prefix: String,
}

impl Config {
    pub fn from_process_env() -> Result<Self> {
        let env_map = env::vars().collect::<HashMap<_, _>>();
        Self::from_env_map(&env_map)
    }

    pub fn from_env_map(env_map: &HashMap<String, String>) -> Result<Self> {
        let port = match env_map.get("PORT").map(String::as_str) {
            Some(raw) if !raw.trim().is_empty() => raw
                .parse::<u16>()
                .map_err(|_| anyhow!("PORT must be a valid u16"))?,
            _ => DEFAULT_PORT,
        };

        let upstream_base_url = env_map
            .get("UPSTREAM_BASE_URL")
            .map(|v| v.trim())
            .filter(|v| !v.is_empty())
            .unwrap_or(DEFAULT_UPSTREAM_BASE_URL)
            .to_string();

        let image_host_mode = env_map
            .get("IMAGE_HOST_MODE")
            .map(|v| v.trim())
            .filter(|v| !v.is_empty())
            .unwrap_or("legacy")
            .to_ascii_lowercase();

        let allowed_proxy_domains = env_map
            .get("ALLOWED_PROXY_DOMAINS")
            .map(String::as_str)
            .map(parse_csv)
            .filter(|domains| !domains.is_empty())
            .unwrap_or_else(default_allowed_proxy_domains);

        let config = Self {
            port,
            upstream_base_url,
            upstream_api_key: env_map
                .get("UPSTREAM_API_KEY")
                .cloned()
                .unwrap_or_default(),
            image_host_mode,
            allowed_proxy_domains,
            public_base_url: env_map
                .get("PUBLIC_BASE_URL")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            proxy_standard_output_urls: parse_bool(env_map.get("PROXY_STANDARD_OUTPUT_URLS"), true),
            upload_timeout_ms: env_map
                .get("UPLOAD_TIMEOUT_MS")
                .and_then(|value| value.trim().parse::<u64>().ok())
                .filter(|value| *value > 0)
                .unwrap_or(10_000),
            legacy_uguu_upload_url: env_map
                .get("LEGACY_UGUU_UPLOAD_URL")
                .map(|v| v.trim())
                .filter(|v| !v.is_empty())
                .unwrap_or("https://uguu.se/upload")
                .to_string(),
            legacy_kefan_upload_url: env_map
                .get("LEGACY_KEFAN_UPLOAD_URL")
                .map(|v| v.trim())
                .filter(|v| !v.is_empty())
                .unwrap_or("https://ai.kefan.cn/api/upload/local")
                .to_string(),
            r2_endpoint: env_map
                .get("R2_ENDPOINT")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            r2_bucket: env_map
                .get("R2_BUCKET")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            r2_access_key_id: env_map
                .get("R2_ACCESS_KEY_ID")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            r2_secret_access_key: env_map
                .get("R2_SECRET_ACCESS_KEY")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            r2_public_base_url: env_map
                .get("R2_PUBLIC_BASE_URL")
                .map(|v| v.trim().to_string())
                .unwrap_or_default(),
            r2_object_prefix: env_map
                .get("R2_OBJECT_PREFIX")
                .map(|v| v.trim().trim_matches('/').to_string())
                .filter(|v| !v.is_empty())
                .unwrap_or_else(|| "images".to_string()),
        };

        validate(&config)?;
        Ok(config)
    }
}

fn validate(config: &Config) -> Result<()> {
    match config.image_host_mode.as_str() {
        "" | "legacy" | "r2" | "r2_then_legacy" => {}
        other => return Err(anyhow!("IMAGE_HOST_MODE must be one of legacy, r2, r2_then_legacy, got {other}")),
    }

    if matches!(config.image_host_mode.as_str(), "r2" | "r2_then_legacy") {
        for (name, value) in [
            ("R2_ENDPOINT", config.r2_endpoint.as_str()),
            ("R2_BUCKET", config.r2_bucket.as_str()),
            ("R2_ACCESS_KEY_ID", config.r2_access_key_id.as_str()),
            ("R2_SECRET_ACCESS_KEY", config.r2_secret_access_key.as_str()),
            ("R2_PUBLIC_BASE_URL", config.r2_public_base_url.as_str()),
        ] {
            if value.trim().is_empty() {
                return Err(anyhow!("{name} is required when IMAGE_HOST_MODE is r2 or r2_then_legacy"));
            }
        }
    }

    Ok(())
}

fn parse_csv(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(ToOwned::to_owned)
        .collect()
}

fn parse_bool(value: Option<&String>, default_value: bool) -> bool {
    match value.map(|v| v.trim().to_ascii_lowercase()) {
        Some(v) if matches!(v.as_str(), "1" | "true" | "yes" | "y" | "on" | "enable" | "enabled") => true,
        Some(v) if matches!(v.as_str(), "0" | "false" | "no" | "n" | "off" | "disable" | "disabled" | "none") => false,
        Some(_) => default_value,
        None => default_value,
    }
}

fn default_allowed_proxy_domains() -> Vec<String> {
    vec![
        "ai.kefan.cn".to_string(),
        "uguu.se".to_string(),
        ".uguu.se".to_string(),
        ".aitohumanize.com".to_string(),
    ]
}
