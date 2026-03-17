package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandleNonStreamResponse_PreservesTextPartWhileDroppingSmallerInlineImages_Active(t *testing.T) {
	smallB64 := mustEncodePatternPNGBase64(t, 32)
	largeB64 := mustEncodePatternPNGBase64(t, 128)
	upstreamBody := makeInlineDataResponseBody(
		map[string]interface{}{
			"text": "generated image",
		},
		map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": "image/png",
				"data":     smallB64,
			},
		},
		map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": "image/png",
				"data":     largeB64,
			},
		},
	)

	app := &App{}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent", nil)
	rr := httptest.NewRecorder()

	app.handleNonStreamResponse(rr, resp, "base64", req, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rr.Code, rr.Body.String())
	}
	parts := extractCandidateParts(t, rr.Body.Bytes())
	if len(parts) != 2 {
		t.Fatalf("expected text part plus largest image, got=%d body=%s", len(parts), rr.Body.String())
	}
	if text, ok := parts[0].(map[string]interface{})["text"].(string); !ok || text != "generated image" {
		t.Fatalf("expected text part to be preserved, got=%#v", parts[0])
	}
	if got := extractInlineDataField(t, parts[1], "data"); got != largeB64 {
		t.Fatalf("expected larger image to remain, got=%q want=%q", got, largeB64)
	}
}
