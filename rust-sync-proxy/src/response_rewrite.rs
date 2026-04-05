use std::collections::HashMap;

use base64::Engine;
use base64::engine::general_purpose::STANDARD;
use serde_json::Value;

use crate::upload::Uploader;

pub fn normalize_gemini_response(mut body: Value) -> Value {
    remove_thought_signatures(&mut body);
    keep_largest_inline_image(body)
}

pub async fn rewrite_inline_data_base64_to_urls(
    mut body: Value,
    uploader: &Uploader,
    public_base_url: &str,
    wrap_legacy_urls: bool,
) -> Value {
    let entries = scan_inline_data_base64_entries(&body);
    let mut replacements = HashMap::new();

    for entry in entries {
        if let Ok(image_bytes) = STANDARD.decode(entry.data.as_bytes()) {
            if let Ok(upload_result) = uploader.upload_image(&image_bytes, &entry.mime_type).await {
                let final_url = if upload_result.provider == "legacy"
                    && wrap_legacy_urls
                    && !public_base_url.trim().is_empty()
                {
                    crate::upload::wrap_proxy_url(public_base_url, &upload_result.url)
                } else {
                    upload_result.url
                };
                replacements.insert(entry, final_url);
            }
        }
    }

    patch_inline_data_urls(&mut body, &replacements);
    body
}

#[derive(Clone, Debug, PartialEq, Eq, Hash)]
struct InlineDataEntry {
    mime_type: String,
    data: String,
}

pub fn remove_thought_signatures(node: &mut Value) {
    match node {
        Value::Object(map) => {
            map.remove("thoughtSignature");
            for child in map.values_mut() {
                remove_thought_signatures(child);
            }
        }
        Value::Array(items) => {
            for child in items {
                remove_thought_signatures(child);
            }
        }
        _ => {}
    }
}

fn is_url_like(value: &str) -> bool {
    value.starts_with("http://") || value.starts_with("https://") || value.starts_with("/proxy/image")
}

fn scan_inline_data_base64_entries(node: &Value) -> Vec<InlineDataEntry> {
    let mut entries = Vec::new();

    fn walk(node: &Value, entries: &mut Vec<InlineDataEntry>) {
        match node {
            Value::Object(map) => {
                if let Some(Value::Object(inline_data)) = map.get("inlineData") {
                    if let (Some(Value::String(data)), Some(Value::String(mime_type))) =
                        (inline_data.get("data"), inline_data.get("mimeType"))
                    {
                        if !is_url_like(data) {
                            let entry = InlineDataEntry {
                                mime_type: mime_type.clone(),
                                data: data.clone(),
                            };
                            if !entries.contains(&entry) {
                                entries.push(entry);
                            }
                        }
                    }
                }

                for child in map.values() {
                    walk(child, entries);
                }
            }
            Value::Array(items) => {
                for child in items {
                    walk(child, entries);
                }
            }
            _ => {}
        }
    }

    walk(node, &mut entries);
    entries
}

fn patch_inline_data_urls(node: &mut Value, replacements: &HashMap<InlineDataEntry, String>) {
    match node {
        Value::Object(map) => {
            if let Some(Value::Object(inline_data)) = map.get_mut("inlineData") {
                if let (Some(Value::String(data)), Some(Value::String(mime_type))) =
                    (inline_data.get("data"), inline_data.get("mimeType"))
                {
                    let entry = InlineDataEntry {
                        mime_type: mime_type.clone(),
                        data: data.clone(),
                    };
                    if let Some(url) = replacements.get(&entry) {
                        inline_data.insert("data".to_string(), Value::String(url.clone()));
                    }
                }
            }

            for child in map.values_mut() {
                patch_inline_data_urls(child, replacements);
            }
        }
        Value::Array(items) => {
            for child in items {
                patch_inline_data_urls(child, replacements);
            }
        }
        _ => {}
    }
}

pub fn keep_largest_inline_image(mut body: Value) -> Value {
    let Some(candidates) = body.get_mut("candidates").and_then(Value::as_array_mut) else {
        return body;
    };

    for candidate in candidates {
        let Some(parts) = candidate
            .get_mut("content")
            .and_then(Value::as_object_mut)
            .and_then(|content| content.get_mut("parts"))
            .and_then(Value::as_array_mut)
        else {
            continue;
        };

        let mut best_index = None;
        let mut best_size = 0usize;

        for (index, part) in parts.iter().enumerate() {
            let Some(inline_data) = part.get("inlineData").and_then(Value::as_object) else {
                continue;
            };
            let Some(data) = inline_data.get("data").and_then(Value::as_str) else {
                continue;
            };
            if data.starts_with("http://") || data.starts_with("https://") || data.starts_with("/proxy/image") {
                continue;
            }
            if data.len() > best_size {
                best_size = data.len();
                best_index = Some(index);
            }
        }

        if let Some(best_index) = best_index {
            let mut retained = Vec::with_capacity(parts.len());
            for (index, part) in parts.iter().enumerate() {
                let is_inline_image = part
                    .get("inlineData")
                    .and_then(Value::as_object)
                    .and_then(|inline_data| inline_data.get("data"))
                    .and_then(Value::as_str)
                    .is_some();

                if !is_inline_image || index == best_index {
                    retained.push(part.clone());
                }
            }
            *parts = retained;
        }
    }

    body
}
