package validation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidateImageGenerationRequestPreservesImageAliases(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		aliasField string
	}{
		{name: "image alias", aliasField: "image"},
		{name: "images alias", aliasField: "images"},
		{name: "reference_images canonical", aliasField: "reference_images"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := newImageGenerationRequest(t, map[string]any{
				"model": tc.aliasField + "-model",
				tc.aliasField: []any{
					"https://example.com/reference.png",
				},
			}, "Bearer sk-image")

			validated, err := ValidateImageGenerationRequest(req)
			if err != nil {
				t.Fatalf("ValidateImageGenerationRequest() error = %v", err)
			}

			if validated.Model != tc.aliasField+"-model" {
				t.Fatalf("Model = %q, want %q", validated.Model, tc.aliasField+"-model")
			}

			var normalized map[string]any
			if err := json.Unmarshal(validated.RequestBodyJSON, &normalized); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}

			if got := normalized["response_format"]; got != "url" {
				t.Fatalf("response_format = %#v, want %q", got, "url")
			}
			images, ok := normalized[tc.aliasField].([]any)
			if !ok || len(images) != 1 || images[0] != "https://example.com/reference.png" {
				t.Fatalf("%s = %#v", tc.aliasField, normalized[tc.aliasField])
			}
		})
	}
}

func TestValidateImageGenerationRequestRejectsInvalidResponseFormat(t *testing.T) {
	t.Parallel()

	req := newImageGenerationRequest(t, map[string]any{
		"model":           "gpt-image-1",
		"response_format": "b64_json",
	}, "Bearer sk-image")

	_, err := ValidateImageGenerationRequest(req)
	assertImageRequestErrorStatus(t, err, http.StatusBadRequest)
}

func TestValidateImageGenerationRequestRejectsNonHTTPReferenceImageURL(t *testing.T) {
	t.Parallel()

	req := newImageGenerationRequest(t, map[string]any{
		"model": "gpt-image-1",
		"image": []any{
			"ftp://example.com/reference.png",
		},
	}, "Bearer sk-image")

	_, err := ValidateImageGenerationRequest(req)
	assertImageRequestErrorStatus(t, err, http.StatusBadRequest)
}

func TestValidateImageGenerationRequestRejectsMissingAuthorization(t *testing.T) {
	t.Parallel()

	req := newImageGenerationRequest(t, map[string]any{
		"model": "gpt-image-1",
	}, "")

	_, err := ValidateImageGenerationRequest(req)
	assertImageRequestErrorStatus(t, err, http.StatusUnauthorized)
}

func TestValidateImageGenerationRequestPreservesImageAliasForForwarding(t *testing.T) {
	t.Parallel()

	req := newImageGenerationRequest(t, map[string]any{
		"model": "gpt-image-2",
		"image": []any{
			"https://example.com/reference.png",
		},
		"response_format": "url",
	}, "Bearer http://upstream.example|sk-image")

	validated, err := ValidateImageGenerationRequest(req)
	if err != nil {
		t.Fatalf("ValidateImageGenerationRequest() error = %v", err)
	}

	var normalized map[string]any
	if err := json.Unmarshal(validated.RequestBodyJSON, &normalized); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	if _, ok := normalized["image"]; !ok {
		t.Fatalf("expected image alias to be preserved in forwarded body: %#v", normalized)
	}
	if _, ok := normalized["reference_images"]; ok {
		t.Fatalf("did not expect reference_images in forwarded body: %#v", normalized)
	}
	if got := normalized["response_format"]; got != "url" {
		t.Fatalf("response_format = %#v, want %q", got, "url")
	}
}

func newImageGenerationRequest(t *testing.T, body map[string]any, authHeader string) *http.Request {
	t.Helper()

	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	return req
}

func assertImageRequestErrorStatus(t *testing.T, err error, wantStatus int) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected validation error")
	}

	requestErr, ok := err.(*RequestError)
	if !ok {
		t.Fatalf("error type = %T, want *RequestError", err)
	}
	if requestErr.StatusCode != wantStatus {
		t.Fatalf("StatusCode = %d, want %d", requestErr.StatusCode, wantStatus)
	}
}
