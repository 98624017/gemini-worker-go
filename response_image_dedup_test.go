//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type dedupUploadSuccessTransport struct {
	mu        sync.Mutex
	callCount int
}

func (t *dedupUploadSuccessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.callCount++
	call := t.callCount
	t.mu.Unlock()

	body := fmt.Sprintf(`{"success":true,"files":[{"url":"https://cdn.example.com/image-%d.png"}]}`, call)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Request:    req,
	}, nil
}

func (t *dedupUploadSuccessTransport) getCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callCount
}

func TestHandleNormalResponse_Base64Output_DropsSmallerDuplicateImage(t *testing.T) {
	smallB64 := dedupMustEncodePatternPNGBase64(t, 32)
	largeB64 := dedupMustEncodePatternPNGBase64(t, 128)

	app := &App{}
	resp := dedupNewJSONResponse(t, dedupInlineImageResponseBody(
		dedupInlineImagePart("image/png", smallB64),
		dedupInlineImagePart("image/png", largeB64),
	))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://localhost/v1beta/models/test:generateContent", nil)

	app.handleNonStreamResponse(rr, resp, "base64", req, nil)

	parts := dedupExtractResponseParts(t, rr.Body.Bytes())
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part after pruning duplicate resolutions, got=%d", len(parts))
	}

	gotInline := parts[0]["inlineData"].(map[string]interface{})
	if gotData, _ := gotInline["data"].(string); gotData != largeB64 {
		t.Fatalf("expected larger image to remain, got len=%d want len=%d", len(gotData), len(largeB64))
	}
}

func TestHandleNormalResponse_URLOutput_DropsSmallerDuplicateImageBeforeUpload(t *testing.T) {
	smallB64 := dedupMustEncodePatternPNGBase64(t, 32)
	largeB64 := dedupMustEncodePatternPNGBase64(t, 128)

	uploadRT := &dedupUploadSuccessTransport{}
	app := &App{
		UploadClient: &http.Client{Transport: uploadRT},
	}
	resp := dedupNewJSONResponse(t, dedupInlineImageResponseBody(
		dedupInlineImagePart("image/png", smallB64),
		dedupInlineImagePart("image/png", largeB64),
	))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://localhost/v1beta/models/test:generateContent?output=url", nil)

	app.handleNonStreamResponse(rr, resp, "url", req, nil)

	parts := dedupExtractResponseParts(t, rr.Body.Bytes())
	if len(parts) != 1 {
		t.Fatalf("expected 1 url image part after pruning duplicate resolutions, got=%d", len(parts))
	}

	gotInline := parts[0]["inlineData"].(map[string]interface{})
	if gotData, _ := gotInline["data"].(string); gotData != "https://cdn.example.com/image-1.png" {
		t.Fatalf("unexpected uploaded url: %q", gotData)
	}
	if got := uploadRT.getCallCount(); got != 1 {
		t.Fatalf("expected only larger image to be uploaded once, got=%d uploads", got)
	}
}

func TestHandleNormalResponse_Base64Output_PreservesDistinctImages(t *testing.T) {
	firstB64 := dedupMustEncodePatternPNGBase64(t, 64)
	secondB64 := dedupMustEncodeCheckerPNGBase64(t, 64)

	app := &App{}
	resp := dedupNewJSONResponse(t, dedupInlineImageResponseBody(
		dedupInlineImagePart("image/png", firstB64),
		dedupInlineImagePart("image/png", secondB64),
	))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://localhost/v1beta/models/test:generateContent", nil)

	app.handleNonStreamResponse(rr, resp, "base64", req, nil)

	parts := dedupExtractResponseParts(t, rr.Body.Bytes())
	if len(parts) != 2 {
		t.Fatalf("expected distinct images to be preserved, got=%d parts", len(parts))
	}
}

func newJSONResponse(t *testing.T, body map[string]interface{}) *http.Response {
	t.Helper()
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal response body: %v", err)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
	}
}

func inlineImageResponseBody(parts ...map[string]interface{}) map[string]interface{} {
	partInterfaces := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		partInterfaces = append(partInterfaces, part)
	}
	return map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": partInterfaces,
				},
			},
		},
	}
}

func inlineImagePart(mimeType string, data string) map[string]interface{} {
	return map[string]interface{}{
		"inlineData": map[string]interface{}{
			"mimeType": mimeType,
			"data":     data,
		},
	}
}

func extractResponseParts(t *testing.T, body []byte) []map[string]interface{} {
	t.Helper()

	var parsed map[string]interface{}
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal response: %v body=%s", err, string(body))
	}

	candidates, ok := parsed["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		t.Fatalf("missing candidates in response: %s", string(body))
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		t.Fatalf("invalid candidate shape: %s", string(body))
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("invalid content shape: %s", string(body))
	}
	rawParts, ok := content["parts"].([]interface{})
	if !ok {
		t.Fatalf("invalid parts shape: %s", string(body))
	}

	parts := make([]map[string]interface{}, 0, len(rawParts))
	for _, rawPart := range rawParts {
		part, ok := rawPart.(map[string]interface{})
		if !ok {
			t.Fatalf("invalid part shape: %s", string(body))
		}
		parts = append(parts, part)
	}
	return parts
}

func mustEncodePatternPNGBase64(t *testing.T, size int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r := uint8((x * 255) / max(size-1, 1))
			g := uint8((y * 255) / max(size-1, 1))
			b := uint8(((x + y) * 255) / max((size*2)-2, 1))
			if (x/max(size/8, 1)+y/max(size/8, 1))%2 == 0 {
				img.Set(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 255 - r, G: 255 - g, B: 255 - b, A: 255})
			}
		}
	}

	return mustEncodePNGBase64(t, img)
}

func mustEncodeCheckerPNGBase64(t *testing.T, size int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	block := max(size/6, 1)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if ((x / block) % 2) == 0 {
				img.Set(x, y, color.RGBA{R: 20, G: 40, B: 220, A: 255})
				continue
			}
			if ((y / block) % 2) == 0 {
				img.Set(x, y, color.RGBA{R: 240, G: 230, B: 30, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 40, G: 200, B: 80, A: 255})
			}
		}
	}

	return mustEncodePNGBase64(t, img)
}

func mustEncodePNGBase64(t *testing.T, img image.Image) string {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
