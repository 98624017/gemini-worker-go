package validation

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"banana-async-gateway/internal/security"
)

type ValidatedImageGenerationRequest struct {
	Model               string
	AuthorizationHeader string
	AuthorizationToken  string
	RequestBody         map[string]any
	RequestBodyJSON     []byte
	ReferenceImageCount int
	DecompressedBytes   int
	ContentEncoding     string
}

func ValidateImageGenerationRequest(r *http.Request) (ValidatedImageGenerationRequest, error) {
	authorizationToken, err := security.NormalizeBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return ValidatedImageGenerationRequest{}, &RequestError{
			StatusCode: http.StatusUnauthorized,
			Code:       "unauthorized",
			Message:    err.Error(),
		}
	}

	contentEncoding := normalizeContentEncoding(r.Header.Get("Content-Encoding"))
	bodyBytes, err := readAndDecodeBody(r.Body, contentEncoding)
	if err != nil {
		var requestErr *RequestError
		if ok := asRequestError(err, &requestErr); ok {
			return ValidatedImageGenerationRequest{}, requestErr
		}
		return ValidatedImageGenerationRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_request_body",
			Message:    err.Error(),
		}
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return ValidatedImageGenerationRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_json",
			Message:    fmt.Sprintf("invalid JSON body: %v", err),
		}
	}

	model, _ := body["model"].(string)
	model = strings.TrimSpace(model)
	if model == "" {
		return ValidatedImageGenerationRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_model",
			Message:    "model is required",
		}
	}

	if err := normalizeImageGenerationRequestBody(body); err != nil {
		return ValidatedImageGenerationRequest{}, err
	}

	normalizedJSON, err := json.Marshal(body)
	if err != nil {
		return ValidatedImageGenerationRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "normalize_request_failed",
			Message:    fmt.Sprintf("normalize request body: %v", err),
		}
	}

	referenceImageCount := 0
	if images, ok := body["reference_images"].([]any); ok {
		referenceImageCount = len(images)
	}

	return ValidatedImageGenerationRequest{
		Model:               model,
		AuthorizationHeader: "Bearer " + authorizationToken,
		AuthorizationToken:  authorizationToken,
		RequestBody:         body,
		RequestBodyJSON:     normalizedJSON,
		ReferenceImageCount: referenceImageCount,
		DecompressedBytes:   len(bodyBytes),
		ContentEncoding:     contentEncoding,
	}, nil
}

func normalizeImageGenerationRequestBody(body map[string]any) error {
	referenceImages, err := extractReferenceImages(body)
	if err != nil {
		return err
	}

	delete(body, "image")
	delete(body, "images")
	body["reference_images"] = referenceImages

	responseFormat := "url"
	if raw, ok := body["response_format"]; ok {
		formatted, ok := raw.(string)
		if !ok || !strings.EqualFold(strings.TrimSpace(formatted), "url") {
			return &RequestError{
				StatusCode: http.StatusBadRequest,
				Code:       "invalid_response_format",
				Message:    "response_format must be url",
			}
		}
	}
	body["response_format"] = responseFormat

	return nil
}

func extractReferenceImages(body map[string]any) ([]any, error) {
	for _, key := range []string{"reference_images", "images", "image"} {
		raw, ok := body[key]
		if !ok {
			continue
		}

		items, ok := raw.([]any)
		if !ok {
			return nil, &RequestError{
				StatusCode: http.StatusBadRequest,
				Code:       "invalid_reference_images",
				Message:    key + " must be an array of absolute http/https URLs",
			}
		}
		if len(items) > maxReferenceImages {
			return nil, &RequestError{
				StatusCode: http.StatusBadRequest,
				Code:       "too_many_reference_images",
				Message:    fmt.Sprintf("reference image count exceeds %d", maxReferenceImages),
			}
		}

		normalized := make([]any, 0, len(items))
		for _, item := range items {
			rawURL, ok := item.(string)
			if !ok {
				return nil, &RequestError{
					StatusCode: http.StatusBadRequest,
					Code:       "invalid_reference_image_url",
					Message:    "reference image URL must be a valid absolute URL",
				}
			}
			trimmed := strings.TrimSpace(rawURL)
			if trimmed == "" || len(trimmed) > maxReferenceURLLength {
				return nil, &RequestError{
					StatusCode: http.StatusBadRequest,
					Code:       "invalid_reference_image_url",
					Message:    "reference image URL must be a valid absolute URL",
				}
			}
			parsed, err := url.Parse(trimmed)
			if err != nil || parsed.Scheme == "" || parsed.Host == "" {
				return nil, &RequestError{
					StatusCode: http.StatusBadRequest,
					Code:       "invalid_reference_image_url",
					Message:    "reference image URL must be a valid absolute URL",
				}
			}
			if parsed.Scheme != "http" && parsed.Scheme != "https" {
				return nil, &RequestError{
					StatusCode: http.StatusBadRequest,
					Code:       "invalid_reference_image_scheme",
					Message:    "reference image URL must use http or https",
				}
			}
			normalized = append(normalized, trimmed)
		}
		return normalized, nil
	}

	return []any{}, nil
}

func asRequestError(err error, target **RequestError) bool {
	if err == nil {
		return false
	}
	requestErr, ok := err.(*RequestError)
	if !ok {
		return false
	}
	*target = requestErr
	return true
}
