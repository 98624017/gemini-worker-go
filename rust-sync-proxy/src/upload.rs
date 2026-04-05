use std::future::Future;
use std::pin::Pin;

use anyhow::{Result, anyhow};

pub type BoxUploadFuture = Pin<Box<dyn Future<Output = Result<UploadResult>> + Send>>;

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ImageHostMode {
    Legacy,
    R2,
    R2ThenLegacy,
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UploadResult {
    pub url: String,
    pub provider: String,
}

pub async fn upload_image_with_mode<R2, Legacy>(
    mode: ImageHostMode,
    data: &[u8],
    mime_type: &str,
    r2_uploader: &R2,
    legacy_uploader: &Legacy,
) -> Result<UploadResult>
where
    R2: Fn(Vec<u8>, String) -> BoxUploadFuture + Sync,
    Legacy: Fn(Vec<u8>, String) -> BoxUploadFuture + Sync,
{
    match mode {
        ImageHostMode::Legacy => legacy_uploader(data.to_vec(), mime_type.to_string()).await,
        ImageHostMode::R2 => r2_uploader(data.to_vec(), mime_type.to_string()).await,
        ImageHostMode::R2ThenLegacy => {
            match r2_uploader(data.to_vec(), mime_type.to_string()).await {
                Ok(result) => Ok(result),
                Err(_) => legacy_uploader(data.to_vec(), mime_type.to_string()).await,
            }
        }
    }
}

pub fn parse_image_host_mode(raw: &str) -> Result<ImageHostMode> {
    match raw.trim().to_ascii_lowercase().as_str() {
        "" | "legacy" => Ok(ImageHostMode::Legacy),
        "r2" => Ok(ImageHostMode::R2),
        "r2_then_legacy" => Ok(ImageHostMode::R2ThenLegacy),
        other => Err(anyhow!("unsupported IMAGE_HOST_MODE {}", other)),
    }
}
