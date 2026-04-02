package validation

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/andybalholm/brotli"

	"banana-async-gateway/internal/security"
)

const (
	maxReferenceImages       = 8
	maxReferenceURLLength    = 4096
	maxDecompressedBodyBytes = 2 * 1024 * 1024
)

type RequestError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *RequestError) Error() string {
	return e.Message
}

type ValidatedGeminiRequest struct {
	Model               string
	AuthorizationHeader string
	AuthorizationToken  string
	RequestBody         map[string]any
	RequestBodyJSON     []byte
	PromptLength        int
	ReferenceImageCount int
	DecompressedBytes   int
	ContentEncoding     string
}

func ValidateGenerateContentRequest(r *http.Request, model string) (ValidatedGeminiRequest, error) {
	authorizationToken, err := security.NormalizeBearerToken(r.Header.Get("Authorization"))
	if err != nil {
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusUnauthorized,
			Code:       "unauthorized",
			Message:    err.Error(),
		}
	}

	model = strings.TrimSpace(model)
	if model == "" {
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_model",
			Message:    "model is required",
		}
	}

	contentEncoding := normalizeContentEncoding(r.Header.Get("Content-Encoding"))
	bodyBytes, err := readAndDecodeBody(r.Body, contentEncoding)
	if err != nil {
		var requestErr *RequestError
		if errors.As(err, &requestErr) {
			return ValidatedGeminiRequest{}, requestErr
		}
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_request_body",
			Message:    err.Error(),
		}
	}

	var body map[string]any
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "invalid_json",
			Message:    fmt.Sprintf("invalid JSON body: %v", err),
		}
	}

	if output := getOutputMode(r.URL.Query().Get("output"), body); output != "url" {
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "output_must_be_url",
			Message:    "output must resolve to url",
		}
	}

	promptLength := countPromptText(body)

	referenceImageCount, err := validateReferenceImageURLs(body)
	if err != nil {
		return ValidatedGeminiRequest{}, err
	}

	normalizedJSON, err := json.Marshal(body)
	if err != nil {
		return ValidatedGeminiRequest{}, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "normalize_request_failed",
			Message:    fmt.Sprintf("normalize request body: %v", err),
		}
	}

	return ValidatedGeminiRequest{
		Model:               model,
		AuthorizationHeader: "Bearer " + authorizationToken,
		AuthorizationToken:  authorizationToken,
		RequestBody:         body,
		RequestBodyJSON:     normalizedJSON,
		PromptLength:        promptLength,
		ReferenceImageCount: referenceImageCount,
		DecompressedBytes:   len(bodyBytes),
		ContentEncoding:     contentEncoding,
	}, nil
}

func normalizeContentEncoding(raw string) string {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	if trimmed == "" {
		return "identity"
	}
	return trimmed
}

func readAndDecodeBody(body io.ReadCloser, contentEncoding string) ([]byte, error) {
	defer body.Close()

	var reader io.Reader = body
	switch contentEncoding {
	case "identity":
	case "gzip":
		gzipReader, err := gzip.NewReader(body)
		if err != nil {
			return nil, &RequestError{
				StatusCode: http.StatusBadRequest,
				Code:       "invalid_gzip_body",
				Message:    fmt.Sprintf("invalid gzip body: %v", err),
			}
		}
		defer gzipReader.Close()
		reader = gzipReader
	case "br":
		reader = brotli.NewReader(body)
	default:
		return nil, &RequestError{
			StatusCode: http.StatusBadRequest,
			Code:       "unsupported_content_encoding",
			Message:    fmt.Sprintf("unsupported content encoding %q", contentEncoding),
		}
	}

	limited := io.LimitReader(reader, maxDecompressedBodyBytes+1)
	bodyBytes, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if len(bodyBytes) > maxDecompressedBodyBytes {
		return nil, &RequestError{
			StatusCode: http.StatusRequestEntityTooLarge,
			Code:       "request_body_too_large",
			Message:    fmt.Sprintf("decompressed request body exceeds %d bytes", maxDecompressedBodyBytes),
		}
	}

	return bodyBytes, nil
}

func getOutputMode(queryOutput string, body map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(queryOutput), "url") {
		return "url"
	}

	if v, ok := body["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
		return "url"
	}

	if genConfig, ok := body["generationConfig"].(map[string]any); ok {
		if imageConfig, ok := genConfig["imageConfig"].(map[string]any); ok {
			if v, ok := imageConfig["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
				return "url"
			}
		}
	}

	if genConfig, ok := body["generation_config"].(map[string]any); ok {
		if imageConfig, ok := genConfig["image_config"].(map[string]any); ok {
			if v, ok := imageConfig["output"].(string); ok && strings.EqualFold(strings.TrimSpace(v), "url") {
				return "url"
			}
		}
	}

	return "base64"
}

func countPromptText(body map[string]any) int {
	total := 0
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case map[string]any:
			for key, child := range typed {
				if key == "text" {
					if text, ok := child.(string); ok {
						total += len(text)
					}
				}
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}

	walk(body)
	return total
}

func validateReferenceImageURLs(body map[string]any) (int, error) {
	count := 0
	var walk func(value any) error
	walk = func(value any) error {
		switch typed := value.(type) {
		case map[string]any:
			if inlineData, ok := typed["inlineData"].(map[string]any); ok {
				data, ok := inlineData["data"].(string)
				if !ok || strings.TrimSpace(data) == "" {
					return &RequestError{
						StatusCode: http.StatusBadRequest,
						Code:       "invalid_reference_image_url",
						Message:    "inlineData.data must be a non-empty string URL",
					}
				}
				if len(data) > maxReferenceURLLength {
					return &RequestError{
						StatusCode: http.StatusBadRequest,
						Code:       "reference_image_url_too_long",
						Message:    fmt.Sprintf("reference image URL exceeds %d characters", maxReferenceURLLength),
					}
				}
				parsed, err := url.Parse(data)
				if err != nil || parsed.Scheme == "" || parsed.Host == "" {
					return &RequestError{
						StatusCode: http.StatusBadRequest,
						Code:       "invalid_reference_image_url",
						Message:    "reference image URL must be a valid absolute URL",
					}
				}
				if parsed.Scheme != "http" && parsed.Scheme != "https" {
					return &RequestError{
						StatusCode: http.StatusBadRequest,
						Code:       "invalid_reference_image_scheme",
						Message:    "reference image URL must use http or https",
					}
				}
				count++
				if count > maxReferenceImages {
					return &RequestError{
						StatusCode: http.StatusBadRequest,
						Code:       "too_many_reference_images",
						Message:    fmt.Sprintf("reference image count exceeds %d", maxReferenceImages),
					}
				}
			}

			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child); err != nil {
					return err
				}
			}
		}
		return nil
	}

	if err := walk(body); err != nil {
		return 0, err
	}

	return count, nil
}
