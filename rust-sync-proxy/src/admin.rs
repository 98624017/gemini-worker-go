use std::collections::BTreeSet;

use serde_json::Value;

pub const ADMIN_MAX_BODY_BYTES_PER_ENTRY: usize = 64 * 1024;

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SanitizedAdminLog {
    pub pretty: String,
    pub image_urls: Vec<String>,
}

pub fn sanitize_json_for_log(raw: &[u8]) -> SanitizedAdminLog {
    if raw.is_empty() {
        return SanitizedAdminLog {
            pretty: String::new(),
            image_urls: Vec::new(),
        };
    }

    let mut root = match serde_json::from_slice::<Value>(raw) {
        Ok(root) => root,
        Err(_) => {
            return SanitizedAdminLog {
                pretty: truncate_for_admin_log(&String::from_utf8_lossy(raw), ADMIN_MAX_BODY_BYTES_PER_ENTRY),
                image_urls: Vec::new(),
            };
        }
    };

    let image_urls = redact_inline_data_and_collect_image_urls(&mut root);
    let pretty = serde_json::to_string_pretty(&root)
        .map(|text| truncate_for_admin_log(&text, ADMIN_MAX_BODY_BYTES_PER_ENTRY))
        .unwrap_or_else(|_| truncate_for_admin_log(&String::from_utf8_lossy(raw), ADMIN_MAX_BODY_BYTES_PER_ENTRY));

    SanitizedAdminLog { pretty, image_urls }
}

pub fn extract_finish_reason(body: &Value) -> Option<String> {
    body.get("candidates")
        .and_then(Value::as_array)
        .and_then(|candidates| candidates.first())
        .and_then(|candidate| candidate.get("finishReason"))
        .and_then(Value::as_str)
        .map(ToOwned::to_owned)
}

fn redact_inline_data_and_collect_image_urls(root: &mut Value) -> Vec<String> {
    let mut urls = BTreeSet::new();

    fn walk(node: &mut Value, urls: &mut BTreeSet<String>) {
        match node {
            Value::Object(map) => {
                for key in ["inlineData", "inline_data"] {
                    if let Some(Value::Object(inline)) = map.get_mut(key) {
                        if let Some(Value::String(data)) = inline.get("data") {
                            if is_image_url(data) {
                                urls.insert(data.trim().to_string());
                            } else if !data.trim().is_empty() {
                                inline.insert(
                                    "data".to_string(),
                                    Value::String(format!("[base64 omitted len={}]", data.len())),
                                );
                            }
                        }
                    }
                }

                for child in map.values_mut() {
                    walk(child, urls);
                }
            }
            Value::Array(items) => {
                for child in items {
                    walk(child, urls);
                }
            }
            _ => {}
        }
    }

    walk(root, &mut urls);
    urls.into_iter().collect()
}

fn is_image_url(value: &str) -> bool {
    value.starts_with("http://")
        || value.starts_with("https://")
        || value.starts_with("/proxy/image")
}

fn truncate_for_admin_log(input: &str, max_bytes: usize) -> String {
    if input.len() <= max_bytes {
        return input.to_string();
    }

    let mut boundary = 0usize;
    for (index, _) in input.char_indices() {
        if index > max_bytes {
            break;
        }
        boundary = index;
    }
    let suffix = "\n...[truncated]";
    format!("{}{}", &input[..boundary], suffix)
}
