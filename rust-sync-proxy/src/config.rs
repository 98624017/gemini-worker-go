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

        Ok(Self {
            port,
            upstream_base_url,
            upstream_api_key: env_map
                .get("UPSTREAM_API_KEY")
                .cloned()
                .unwrap_or_default(),
            image_host_mode,
            allowed_proxy_domains,
        })
    }
}

fn parse_csv(raw: &str) -> Vec<String> {
    raw.split(',')
        .map(str::trim)
        .filter(|part| !part.is_empty())
        .map(ToOwned::to_owned)
        .collect()
}

fn default_allowed_proxy_domains() -> Vec<String> {
    vec![
        "ai.kefan.cn".to_string(),
        "uguu.se".to_string(),
        ".uguu.se".to_string(),
        ".aitohumanize.com".to_string(),
    ]
}
