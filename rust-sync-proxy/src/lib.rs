pub mod admin;
pub mod cache;
pub mod config;
pub mod http;
pub mod image_io;
pub mod proxy_image;
pub mod request_rewrite;
pub mod response_rewrite;
pub mod stream_rewrite;
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
        public_base_url: String::new(),
        proxy_standard_output_urls: true,
        upload_timeout_ms: 10_000,
        legacy_uguu_upload_url: "https://uguu.se/upload".to_string(),
        legacy_kefan_upload_url: "https://ai.kefan.cn/api/upload/local".to_string(),
        r2_endpoint: String::new(),
        r2_bucket: String::new(),
        r2_access_key_id: String::new(),
        r2_secret_access_key: String::new(),
        r2_public_base_url: String::new(),
        r2_object_prefix: "images".to_string(),
    }
}
