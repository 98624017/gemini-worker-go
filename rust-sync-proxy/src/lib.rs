pub mod cache;
pub mod config;
pub mod http;
pub mod image_io;
pub mod proxy_image;
pub mod request_rewrite;
pub mod response_rewrite;
pub mod upload;
pub mod upstream;

pub use http::build_router;

use config::Config;

pub fn test_config() -> Config {
    Config {
        port: 8787,
        upstream_base_url: "https://magic666.top".to_string(),
        upstream_api_key: "test-upstream-key".to_string(),
        image_host_mode: "legacy".to_string(),
        allowed_proxy_domains: vec![
            "ai.kefan.cn".to_string(),
            "uguu.se".to_string(),
            ".uguu.se".to_string(),
            ".aitohumanize.com".to_string(),
        ],
    }
}
