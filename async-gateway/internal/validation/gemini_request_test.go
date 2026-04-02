package validation

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
)

func TestValidateGenerateContentRequestRejectsMissingOrMalformedAuthorization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		header string
	}{
		{name: "missing auth", header: ""},
		{name: "malformed auth", header: "Basic abc"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newGeminiRequest(t, makeValidGeminiBody("draw cat", "https://example.com/a.png"), tc.header, "", "identity")
			_, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
			assertRequestError(t, err, http.StatusUnauthorized)
		})
	}
}

func TestValidateGenerateContentRequestRejectsOutputNotURL(t *testing.T) {
	t.Parallel()

	body := makeValidGeminiBody("draw cat", "https://example.com/a.png")
	body["generationConfig"].(map[string]any)["imageConfig"].(map[string]any)["output"] = "base64"

	req := newGeminiRequest(t, body, "Bearer sk-test", "", "identity")
	_, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	assertRequestError(t, err, http.StatusBadRequest)
}

func TestValidateGenerateContentRequestAllowsLongPrompt(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody(strings.Repeat("a", 4001), "https://example.com/a.png"),
		"Bearer sk-test",
		"",
		"identity",
	)

	validated, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	if err != nil {
		t.Fatalf("ValidateGenerateContentRequest() error = %v", err)
	}
	if validated.PromptLength != 4001 {
		t.Fatalf("PromptLength = %d, want %d", validated.PromptLength, 4001)
	}
}

func TestValidateGenerateContentRequestRejectsTooManyReferenceImages(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody("draw cat", makeSequentialURLs(9)...),
		"Bearer sk-test",
		"",
		"identity",
	)

	_, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	assertRequestError(t, err, http.StatusBadRequest)
}

func TestValidateGenerateContentRequestRejectsUnsupportedReferenceImageScheme(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody("draw cat", "ftp://example.com/a.png"),
		"Bearer sk-test",
		"",
		"identity",
	)

	_, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	assertRequestError(t, err, http.StatusBadRequest)
}

func TestValidateGenerateContentRequestRejectsOversizedDecompressedBody(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody(strings.Repeat("b", maxDecompressedBodyBytes+1), "https://example.com/a.png"),
		"Bearer sk-test",
		"",
		"identity",
	)

	_, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	assertRequestError(t, err, http.StatusRequestEntityTooLarge)
}

func TestValidateGenerateContentRequestSupportsBrotliAndQueryOutput(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody("draw cat", "https://example.com/a.png"),
		"Bearer sk-live",
		"output=url",
		"br",
	)

	validated, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	if err != nil {
		t.Fatalf("ValidateGenerateContentRequest() error = %v", err)
	}

	if validated.AuthorizationToken != "sk-live" {
		t.Fatalf("AuthorizationToken = %q, want %q", validated.AuthorizationToken, "sk-live")
	}
	if validated.PromptLength != len("draw cat") {
		t.Fatalf("PromptLength = %d, want %d", validated.PromptLength, len("draw cat"))
	}
	if validated.ReferenceImageCount != 1 {
		t.Fatalf("ReferenceImageCount = %d, want 1", validated.ReferenceImageCount)
	}
	if !bytes.Contains(validated.RequestBodyJSON, []byte(`"draw cat"`)) {
		t.Fatalf("normalized request body missing prompt text: %s", string(validated.RequestBodyJSON))
	}
}

func TestValidateGenerateContentRequestSupportsGzipBodyOutputURL(t *testing.T) {
	t.Parallel()

	req := newGeminiRequest(
		t,
		makeValidGeminiBody("draw cat", "https://example.com/a.png"),
		"Bearer sk-gzip",
		"",
		"gzip",
	)

	validated, err := ValidateGenerateContentRequest(req, "gemini-3-pro-image-preview")
	if err != nil {
		t.Fatalf("ValidateGenerateContentRequest() error = %v", err)
	}

	if validated.ContentEncoding != "gzip" {
		t.Fatalf("ContentEncoding = %q, want %q", validated.ContentEncoding, "gzip")
	}
}

func newGeminiRequest(t *testing.T, body map[string]any, authHeader, rawQuery, encoding string) *http.Request {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var reader io.Reader
	switch encoding {
	case "", "identity":
		reader = bytes.NewReader(payload)
	case "gzip":
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		if _, err := zw.Write(payload); err != nil {
			t.Fatalf("gzip write error = %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("gzip close error = %v", err)
		}
		reader = &buf
	case "br":
		var buf bytes.Buffer
		zw := brotli.NewWriter(&buf)
		if _, err := zw.Write(payload); err != nil {
			t.Fatalf("brotli write error = %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("brotli close error = %v", err)
		}
		reader = &buf
	default:
		t.Fatalf("unsupported test encoding %q", encoding)
	}

	target := "/v1beta/models/gemini-3-pro-image-preview:generateContent"
	if rawQuery != "" {
		target += "?" + rawQuery
	}

	req := httptest.NewRequest(http.MethodPost, target, reader)
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if encoding != "" && encoding != "identity" {
		req.Header.Set("Content-Encoding", encoding)
	}

	return req
}

func makeValidGeminiBody(prompt string, urls ...string) map[string]any {
	parts := []any{map[string]any{"text": prompt}}
	for _, imageURL := range urls {
		parts = append(parts, map[string]any{
			"inlineData": map[string]any{
				"data":     imageURL,
				"mimeType": "image/png",
			},
		})
	}

	return map[string]any{
		"contents": []any{
			map[string]any{
				"role":  "user",
				"parts": parts,
			},
		},
		"generationConfig": map[string]any{
			"imageConfig": map[string]any{
				"output": "url",
			},
		},
	}
}

func makeSequentialURLs(count int) []string {
	urls := make([]string, 0, count)
	for i := 0; i < count; i++ {
		urls = append(urls, "https://example.com/image-"+string(rune('a'+i))+".png")
	}
	return urls
}

func assertRequestError(t *testing.T, err error, wantStatus int) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected validation error")
	}

	var requestErr *RequestError
	if !errors.As(err, &requestErr) {
		t.Fatalf("expected RequestError, got %T (%v)", err, err)
	}
	if requestErr.StatusCode != wantStatus {
		t.Fatalf("StatusCode = %d, want %d", requestErr.StatusCode, wantStatus)
	}
}
