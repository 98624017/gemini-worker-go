use serde_json::Value;

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
