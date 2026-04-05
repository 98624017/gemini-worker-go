use bytes::Bytes;

#[derive(Clone, Debug)]
pub struct CachedInlineData {
    pub mime_type: String,
    pub bytes: Bytes,
}

pub trait InlineDataCache: Send + Sync {
    fn get(&self, _url: &str) -> Option<CachedInlineData> {
        None
    }

    fn set(&self, _url: &str, _value: CachedInlineData) {}
}

#[derive(Default)]
pub struct NoopInlineDataCache;

impl InlineDataCache for NoopInlineDataCache {}
