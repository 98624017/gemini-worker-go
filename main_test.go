package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingTransport struct {
	mu          sync.Mutex
	lastURL     string
	callCount   int
	statusCode  int
	contentType string
	body        []byte
}

type upstreamCaptureTransport struct {
	mu        sync.Mutex
	lastURL   string
	auth      string
	apiKey    string
	callCount int
}

func (t *upstreamCaptureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.lastURL = req.URL.String()
	t.auth = req.Header.Get("Authorization")
	t.apiKey = req.Header.Get("x-goog-api-key")
	t.callCount++
	t.mu.Unlock()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func (t *upstreamCaptureTransport) snapshot() (lastURL string, auth string, apiKey string, callCount int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastURL, t.auth, t.apiKey, t.callCount
}

func TestLoadConfigWithEnv_DefaultImageHostModeIsLegacy(t *testing.T) {
	getenv := func(key string) string { return "" }
	cfg, err := loadConfigWithEnvValidated(getenv, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ImageHostMode != "legacy" {
		t.Fatalf("ImageHostMode=%q want legacy", cfg.ImageHostMode)
	}
}

func TestLoadConfigWithEnv_R2ModeRequiresConfig(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE": "r2",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "R2_ENDPOINT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigWithEnv_RejectsInvalidImageHostMode(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE": "bad-mode",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestLoadConfigWithEnv_R2ModeRejectsInvalidEndpoint(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE":      "r2",
		"R2_ENDPOINT":          "ftp://invalid-endpoint",
		"R2_BUCKET":            "images",
		"R2_ACCESS_KEY_ID":     "key",
		"R2_SECRET_ACCESS_KEY": "secret",
		"R2_PUBLIC_BASE_URL":   "https://img.example.com",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "R2_ENDPOINT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadConfigWithEnv_R2ModeRejectsInvalidPublicBaseURL(t *testing.T) {
	env := map[string]string{
		"IMAGE_HOST_MODE":      "r2",
		"R2_ENDPOINT":          "https://example.r2.cloudflarestorage.com",
		"R2_BUCKET":            "images",
		"R2_ACCESS_KEY_ID":     "key",
		"R2_SECRET_ACCESS_KEY": "secret",
		"R2_PUBLIC_BASE_URL":   "not-a-url",
	}
	getenv := func(key string) string { return env[key] }
	_, err := loadConfigWithEnvValidated(getenv, 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "R2_PUBLIC_BASE_URL") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestHandleGeminiRequest_InvalidDualUpstreamToken_Returns400(t *testing.T) {
	rt := &upstreamCaptureTransport{}
	app := &App{
		Config:         Config{UpstreamBaseURL: "https://default.example", UpstreamAPIKey: "env-key"},
		UpstreamClient: &http.Client{Transport: rt},
	}

	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent", strings.NewReader(`{"contents":[]}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "https://a.example|key-a,not-valid")
	rr := httptest.NewRecorder()

	app.handleGeminiRequest(rr, req, false)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusBadRequest, rr.Body.String())
	}
	if _, _, _, calls := rt.snapshot(); calls != 0 {
		t.Fatalf("upstream should not be called, got=%d", calls)
	}
	if !strings.Contains(rr.Body.String(), "Invalid upstream apiKey") {
		t.Fatalf("unexpected body: %s", rr.Body.String())
	}
}

func TestHandleGeminiRequest_DualUpstreamToken_UsesSecondConfigFor4K(t *testing.T) {
	rt := &upstreamCaptureTransport{}
	app := &App{
		Config:         Config{UpstreamBaseURL: "https://default.example", UpstreamAPIKey: "env-key"},
		UpstreamClient: &http.Client{Transport: rt},
	}

	reqBody := `{"generationConfig":{"imageConfig":{"imageSize":"4K"}},"contents":[]}`
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent", strings.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", "https://a.example|key-a,https://b.example|key-b")
	rr := httptest.NewRecorder()

	app.handleGeminiRequest(rr, req, false)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	lastURL, auth, apiKey, calls := rt.snapshot()
	if calls != 1 {
		t.Fatalf("upstream calls=%d want=1", calls)
	}
	if lastURL != "https://b.example/v1beta/models/test:generateContent" {
		t.Fatalf("lastURL=%q want=%q", lastURL, "https://b.example/v1beta/models/test:generateContent")
	}
	if auth != "Bearer key-b" {
		t.Fatalf("Authorization=%q want=%q", auth, "Bearer key-b")
	}
	if apiKey != "key-b" {
		t.Fatalf("x-goog-api-key=%q want=%q", apiKey, "key-b")
	}
}

func TestHandler_LogsActualStatusCode(t *testing.T) {
	oldWriter := log.Writer()
	oldFlags := log.Flags()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(oldWriter)
		log.SetFlags(oldFlags)
	}()

	app := &App{}
	req := httptest.NewRequest(http.MethodGet, "http://localhost/not-found", nil)
	rr := httptest.NewRecorder()

	app.Handler(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want=%d", rr.Code, http.StatusNotFound)
	}
	if !strings.Contains(buf.String(), "GET /not-found 404 ") {
		t.Fatalf("expected log to contain actual status code, got=%q", buf.String())
	}
}

func TestUploadImageBytesToURL_LegacyModeUsesLegacyUploader(t *testing.T) {
	app := &App{
		Config: Config{ImageHostMode: "legacy"},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{URL: "https://legacy.example/a.png", Provider: "legacy"}, nil
		},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			t.Fatal("r2 uploader should not be called")
			return uploadResult{}, nil
		},
	}

	got, err := app.uploadImageBytesToURL([]byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "legacy" {
		t.Fatalf("Provider=%q want legacy", got.Provider)
	}
}

func TestUploadImageBytesToURL_R2ThenLegacyFallsBack(t *testing.T) {
	app := &App{
		Config: Config{ImageHostMode: "r2_then_legacy"},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{}, errors.New("r2 down")
		},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{URL: "https://legacy.example/a.png", Provider: "legacy"}, nil
		},
	}

	got, err := app.uploadImageBytesToURL([]byte("img"), "image/png")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Provider != "legacy" {
		t.Fatalf("Provider=%q want legacy", got.Provider)
	}
}

func TestBuildR2ObjectKey_UsesDatePrefixAndExtension(t *testing.T) {
	now := time.Date(2026, 3, 31, 10, 20, 30, 123*int(time.Millisecond), time.UTC)
	got := buildR2ObjectKey("images", "image/png", now, "abcd1234")
	want := fmt.Sprintf("images/2026/03/31/%d-abcd1234.png", now.UnixMilli())
	if got != want {
		t.Fatalf("buildR2ObjectKey()=%q want %q", got, want)
	}
}

func TestUploadToR2_ReturnsPublicURL(t *testing.T) {
	now := time.Date(2026, 3, 31, 10, 20, 30, 123*int(time.Millisecond), time.UTC)
	expectedKey := fmt.Sprintf("images/2026/03/31/%d-test.png", now.UnixMilli())

	var putKey string
	var putBody []byte
	var putMimeType string

	app := &App{
		Config: Config{
			R2ObjectPrefix:  "images",
			R2PublicBaseURL: "https://img.example.com/",
			UploadTimeout:   2 * time.Second,
		},
		nowFunc: func() time.Time {
			return now
		},
		randomHexFunc: func(n int) (string, error) {
			if n != 4 {
				t.Fatalf("randomHex size=%d want 4", n)
			}
			return "test", nil
		},
		r2PutObjectFunc: func(ctx context.Context, key string, body []byte, mimeType string) error {
			putKey = key
			putBody = append([]byte(nil), body...)
			putMimeType = mimeType
			return nil
		},
	}

	got, err := app.uploadToR2([]byte("png-bytes"), "image/png")
	if err != nil {
		t.Fatalf("uploadToR2 returned error: %v", err)
	}
	if putKey != expectedKey {
		t.Fatalf("put key=%q want %q", putKey, expectedKey)
	}
	if string(putBody) != "png-bytes" {
		t.Fatalf("put body=%q want %q", string(putBody), "png-bytes")
	}
	if putMimeType != "image/png" {
		t.Fatalf("put mimeType=%q want image/png", putMimeType)
	}
	if got.URL != "https://img.example.com/"+expectedKey {
		t.Fatalf("result url=%q want %q", got.URL, "https://img.example.com/"+expectedKey)
	}
	if got.Provider != "r2" {
		t.Fatalf("result provider=%q want r2", got.Provider)
	}
}

func TestConvertInlineDataBase64ToUrlInResponse_R2ResultSkipsProxyWrap(t *testing.T) {
	root := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"parts": []interface{}{
						map[string]interface{}{
							"inlineData": map[string]interface{}{
								"mimeType": "image/png",
								"data":     base64.StdEncoding.EncodeToString([]byte("hello")),
							},
						},
					},
				},
			},
		},
	}

	app := &App{
		Config: Config{
			ImageHostMode:           "r2",
			PublicBaseURL:           "https://proxy.example.com",
			ProxyStandardOutputURLs: true,
		},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{
				URL:      "https://img.example.com/images/2026/03/31/a.png",
				Provider: "r2",
			}, nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)

	if err := app.convertInlineDataBase64ToUrlInResponse(root, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := extractInlineDataField(t, extractCandidateParts(t, mustMarshalJSONForTest(t, root))[0], "data")
	want := "https://img.example.com/images/2026/03/31/a.png"
	if got != want {
		t.Fatalf("result data=%q want %q", got, want)
	}
}

func TestConvertInlineDataBase64ToUrlInResponse_LegacyResultStillWrapsProxy(t *testing.T) {
	root := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"parts": []interface{}{
						map[string]interface{}{
							"inlineData": map[string]interface{}{
								"mimeType": "image/png",
								"data":     base64.StdEncoding.EncodeToString([]byte("hello")),
							},
						},
					},
				},
			},
		},
	}

	app := &App{
		Config: Config{
			ImageHostMode:           "legacy",
			PublicBaseURL:           "https://proxy.example.com",
			ProxyStandardOutputURLs: true,
		},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{
				URL:      "https://legacy.example/a.png",
				Provider: "legacy",
			}, nil
		},
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)

	if err := app.convertInlineDataBase64ToUrlInResponse(root, req); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := extractInlineDataField(t, extractCandidateParts(t, mustMarshalJSONForTest(t, root))[0], "data")
	want := "https://proxy.example.com/proxy/image?url=" + url.QueryEscape("https://legacy.example/a.png")
	if got != want {
		t.Fatalf("result data=%q want %q", got, want)
	}
}

func mustMarshalJSONForTest(t *testing.T, v interface{}) []byte {
	t.Helper()
	out, err := marshalJSON(v)
	if err != nil {
		t.Fatalf("marshalJSON failed: %v", err)
	}
	return out
}

func TestHandleNonStreamResponse_OutputURL_R2ThenLegacyFallbackUsesLegacyURLRules(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("hello"))
	upstreamBody := makeInlineDataResponseBody(
		map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": "image/png",
				"data":     b64,
			},
		},
	)

	app := &App{
		Config: Config{
			ImageHostMode:           "r2_then_legacy",
			PublicBaseURL:           "https://proxy.example.com",
			ProxyStandardOutputURLs: true,
		},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{}, errors.New("r2 down")
		},
		legacyUploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{
				URL:      "https://legacy.example/a.png",
				Provider: "legacy",
			}, nil
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)
	rr := httptest.NewRecorder()

	app.handleNonStreamResponse(rr, resp, "url", req, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rr.Code, rr.Body.String())
	}
	parts := extractCandidateParts(t, rr.Body.Bytes())
	got := extractInlineDataField(t, parts[0], "data")
	want := "https://proxy.example.com/proxy/image?url=" + url.QueryEscape("https://legacy.example/a.png")
	if got != want {
		t.Fatalf("result data=%q want %q", got, want)
	}
}

func TestHandleNonStreamResponse_OutputURL_R2FailureStillFailOpen(t *testing.T) {
	b64 := base64.StdEncoding.EncodeToString([]byte("hello"))
	upstreamBody := makeInlineDataResponseBody(
		map[string]interface{}{
			"inlineData": map[string]interface{}{
				"mimeType": "image/png",
				"data":     b64,
			},
		},
	)

	app := &App{
		Config: Config{
			ImageHostMode:           "r2",
			PublicBaseURL:           "https://proxy.example.com",
			ProxyStandardOutputURLs: true,
		},
		r2UploadFunc: func(data []byte, mimeType string) (uploadResult, error) {
			return uploadResult{}, errors.New("r2 down")
		},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)
	rr := httptest.NewRecorder()

	app.handleNonStreamResponse(rr, resp, "url", req, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rr.Code, rr.Body.String())
	}
	parts := extractCandidateParts(t, rr.Body.Bytes())
	got := extractInlineDataField(t, parts[0], "data")
	if got != b64 {
		t.Fatalf("expected fail-open to preserve base64, got=%q want=%q", got, b64)
	}
}

func (t *recordingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.lastURL = req.URL.String()
	t.callCount++
	t.mu.Unlock()

	status := t.statusCode
	if status == 0 {
		status = http.StatusOK
	}
	ct := t.contentType
	if ct == "" {
		ct = "image/jpeg"
	}
	b := t.body
	if b == nil {
		b = []byte("hello")
	}

	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", ct)
	return resp, nil
}

func (t *recordingTransport) getLastURL() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lastURL
}

func (t *recordingTransport) getCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callCount
}

type blockingTransport struct {
	mu          sync.Mutex
	callCount   int
	contentType string
	body        []byte
	releaseCh   <-chan struct{}
}

func (t *blockingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.callCount++
	t.mu.Unlock()

	select {
	case <-req.Context().Done():
		return nil, req.Context().Err()
	case <-t.releaseCh:
	}

	ct := t.contentType
	if ct == "" {
		ct = "image/jpeg"
	}
	b := t.body
	if b == nil {
		b = []byte("slow-image")
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(b)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", ct)
	return resp, nil
}

func (t *blockingTransport) getCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callCount
}

type uploadSuccessTransport struct {
	mu        sync.Mutex
	callCount int
}

func (t *uploadSuccessTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.mu.Lock()
	t.callCount++
	callCount := t.callCount
	t.mu.Unlock()

	body := []byte(`{"success":true,"files":[{"url":"https://img.example.com/upload-` + strconv.Itoa(callCount) + `.jpg"}]}`)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func (t *uploadSuccessTransport) getCallCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.callCount
}

func makeInlineDataResponseBody(parts ...map[string]interface{}) []byte {
	body := map[string]interface{}{
		"candidates": []interface{}{
			map[string]interface{}{
				"content": map[string]interface{}{
					"role":  "model",
					"parts": toInterfaceSlice(parts),
				},
			},
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func toInterfaceSlice(parts []map[string]interface{}) []interface{} {
	out := make([]interface{}, 0, len(parts))
	for _, part := range parts {
		out = append(out, part)
	}
	return out
}

func extractCandidateParts(t *testing.T, body []byte) []interface{} {
	t.Helper()

	var root map[string]interface{}
	if err := json.Unmarshal(body, &root); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}

	candidates, ok := root["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		t.Fatalf("response missing candidates: %s", string(body))
	}
	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		t.Fatalf("candidate[0] is not an object: %T", candidates[0])
	}
	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		t.Fatalf("candidate content is not an object: %T", candidate["content"])
	}
	parts, ok := content["parts"].([]interface{})
	if !ok {
		t.Fatalf("candidate parts is not an array: %T", content["parts"])
	}
	return parts
}

func extractInlineDataField(t *testing.T, part interface{}, field string) string {
	t.Helper()

	partMap, ok := part.(map[string]interface{})
	if !ok {
		t.Fatalf("part is not an object: %T", part)
	}
	inlineData, ok := partMap["inlineData"].(map[string]interface{})
	if !ok {
		t.Fatalf("part.inlineData is not an object: %T", partMap["inlineData"])
	}
	value, _ := inlineData[field].(string)
	return value
}

func mustEncodePatternPNGBase64(t *testing.T, size int) string {
	t.Helper()

	img := image.NewRGBA(image.Rect(0, 0, size, size))
	cellSize := size / 8
	if cellSize < 1 {
		cellSize = 1
	}
	denominator := size - 1
	if denominator < 1 {
		denominator = 1
	}
	colorDenominator := (size * 2) - 2
	if colorDenominator < 1 {
		colorDenominator = 1
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r := uint8((x * 255) / denominator)
			g := uint8((y * 255) / denominator)
			b := uint8(((x + y) * 255) / colorDenominator)
			if ((x/cellSize)+(y/cellSize))%2 == 0 {
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
	cellSize := size / 4
	if cellSize < 1 {
		cellSize = 1
	}

	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			if ((x/cellSize)+(y/cellSize))%2 == 0 {
				img.Set(x, y, color.RGBA{R: 15, G: 15, B: 15, A: 255})
			} else {
				img.Set(x, y, color.RGBA{R: 240, G: 240, B: 240, A: 255})
			}
		}
	}

	return mustEncodePNGBase64(t, img)
}

func mustEncodePNGBase64(t *testing.T, img image.Image) string {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png failed: %v", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestHostnameMatchesDomainPatterns(t *testing.T) {
	host := "miratoon.oss-cn-hangzhou.aliyuncs.com"

	if !hostnameMatchesDomainPatterns(host, []string{"miratoon.oss-cn-hangzhou.aliyuncs.com"}) {
		t.Fatalf("expected exact match to be true")
	}
	if !hostnameMatchesDomainPatterns(host, []string{".oss-cn-hangzhou.aliyuncs.com"}) {
		t.Fatalf("expected suffix match to be true")
	}
	if hostnameMatchesDomainPatterns(host, []string{"oss-cn-hangzhou.aliyuncs.com"}) {
		t.Fatalf("expected non-suffix, non-exact match to be false")
	}
	if !hostnameMatchesDomainPatterns("OSS-CN-HANGZHOU.ALIYUNCS.COM", []string{".oss-cn-hangzhou.aliyuncs.com"}) {
		t.Fatalf("expected case-insensitive match to be true")
	}
}

func TestFetchImageUrlAsInlineData_ExternalProxyDomain_RewritesURL(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}

	app := &App{
		Config: Config{
			ImageFetchExternalProxyDomains: []string{"miratoon.oss-cn-hangzhou.aliyuncs.com"},
		},
		ImageFetchClient: &http.Client{Transport: rt},
	}

	mime, b64, _, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("unexpected mimeType: %q", mime)
	}
	if b64 != base64.StdEncoding.EncodeToString(body) {
		t.Fatalf("unexpected base64: %q", b64)
	}

	wantFetchURL := ExternalImageFetchProxyPrefix + url.QueryEscape(rawURL)
	if got := rt.getLastURL(); got != wantFetchURL {
		t.Fatalf("unexpected fetch url. got=%q want=%q", got, wantFetchURL)
	}
}

func TestFetchImageUrlAsInlineData_NoExternalProxyDomain_DoesNotRewriteURL(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"

	rt := &recordingTransport{}
	app := &App{
		Config:           Config{ImageFetchExternalProxyDomains: nil},
		ImageFetchClient: &http.Client{Transport: rt},
	}

	_, _, _, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if got := rt.getLastURL(); got != rawURL {
		t.Fatalf("unexpected fetch url. got=%q want=%q", got, rawURL)
	}
}

func TestHandleProxyImage_ExternalProxyDomain_RewritesFetchURL(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}

	app := &App{
		Config: Config{
			AllowedDomains:                 []string{"miratoon.oss-cn-hangzhou.aliyuncs.com"},
			ImageFetchExternalProxyDomains: []string{"miratoon.oss-cn-hangzhou.aliyuncs.com"},
		},
		ImageFetchClient: &http.Client{Transport: rt},
	}

	req := httptest.NewRequest("GET", "http://localhost/proxy/image?url="+url.QueryEscape(rawURL), nil)
	rr := httptest.NewRecorder()

	app.handleProxyImage(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%q", rr.Code, rr.Body.String())
	}
	if gotCT := rr.Header().Get("Content-Type"); gotCT != "image/jpeg" {
		t.Fatalf("unexpected content-type: %q", gotCT)
	}
	if got := rr.Body.Bytes(); string(got) != string(body) {
		t.Fatalf("unexpected body: %q", string(got))
	}

	wantFetchURL := ExternalImageFetchProxyPrefix + url.QueryEscape(rawURL)
	if got := rt.getLastURL(); got != wantFetchURL {
		t.Fatalf("unexpected fetch url. got=%q want=%q", got, wantFetchURL)
	}
}

func TestFetchImageUrlAsInlineData_DiskCache_HitSkipsNetwork(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}

	cache, err := newInlineDataURLDiskCache(t.TempDir(), 1*time.Hour, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	app := &App{
		Config:             Config{},
		ImageFetchClient:   &http.Client{Transport: rt},
		InlineDataURLCache: cache,
	}

	_, _, fromCache1, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fromCache1 {
		t.Fatalf("expected first fetch to be from network, got fromCache=true")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("unexpected network call count after first fetch: %d", got)
	}

	_, _, fromCache2, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !fromCache2 {
		t.Fatalf("expected second fetch to hit cache, got fromCache=false")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected cache hit to avoid second network call, got=%d", got)
	}
}

func TestFetchImageUrlAsInlineData_DiskCache_TTLExpires(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}

	cache, err := newInlineDataURLDiskCache(t.TempDir(), 20*time.Millisecond, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	app := &App{
		Config:             Config{},
		ImageFetchClient:   &http.Client{Transport: rt},
		InlineDataURLCache: cache,
	}

	_, _, fromCache1, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fromCache1 {
		t.Fatalf("expected first fetch to be from network, got fromCache=true")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("unexpected network call count after first fetch: %d", got)
	}

	time.Sleep(40 * time.Millisecond)

	_, _, fromCache2, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fromCache2 {
		t.Fatalf("expected second fetch after TTL to re-fetch, got fromCache=true")
	}
	if got := rt.getCallCount(); got != 2 {
		t.Fatalf("expected cache expiration to trigger re-fetch, got=%d", got)
	}
}

func TestFetchImageUrlAsInlineData_DiskCache_HitRefreshesTTL(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}

	cache, err := newInlineDataURLDiskCache(t.TempDir(), 120*time.Millisecond, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	app := &App{
		Config:             Config{},
		ImageFetchClient:   &http.Client{Transport: rt},
		InlineDataURLCache: cache,
	}

	_, _, fromCache1, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if fromCache1 {
		t.Fatalf("expected first fetch to be from network, got fromCache=true")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("unexpected network call count after first fetch: %d", got)
	}

	time.Sleep(50 * time.Millisecond)

	_, _, fromCache2, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !fromCache2 {
		t.Fatalf("expected second fetch to hit cache, got fromCache=false")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected cache hit to avoid second network call, got=%d", got)
	}

	// Total elapsed from first fetch is now >120ms, but elapsed from second hit is ~80ms.
	// With sliding TTL on hit, this third fetch should still hit cache.
	time.Sleep(80 * time.Millisecond)

	_, _, fromCache3, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("fetch failed: %v", err)
	}
	if !fromCache3 {
		t.Fatalf("expected third fetch to remain cached due to ttl refresh on hit, got fromCache=false")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected ttl refresh hit to avoid re-fetch, got=%d", got)
	}
}

func TestConvertRequestInlineDataUrlsToBase64_ObserverMarksCacheHit(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	body := []byte("image-bytes")

	rt := &recordingTransport{
		contentType: "image/jpeg",
		body:        body,
	}
	cache, err := newInlineDataURLDiskCache(t.TempDir(), 1*time.Hour, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	app := &App{
		ImageFetchClient:   &http.Client{Transport: rt},
		InlineDataURLCache: cache,
	}

	makeBody := func() map[string]interface{} {
		return map[string]interface{}{
			"contents": []interface{}{
				map[string]interface{}{
					"parts": []interface{}{
						map[string]interface{}{
							"inlineData": map[string]interface{}{
								"data": rawURL,
							},
						},
					},
				},
			},
		}
	}

	var mu sync.Mutex
	fromCacheByURL := make(map[string]bool)
	observer := func(u string, fromCache bool) {
		mu.Lock()
		fromCacheByURL[u] = fromCache
		mu.Unlock()
	}

	if err := app.convertRequestInlineDataUrlsToBase64WithObserver(makeBody(), observer); err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected 1 network call after first convert, got=%d", got)
	}
	mu.Lock()
	firstHit := fromCacheByURL[rawURL]
	mu.Unlock()
	if firstHit {
		t.Fatalf("expected first convert to be from network, got fromCache=true")
	}

	fromCacheByURL = make(map[string]bool)
	if err := app.convertRequestInlineDataUrlsToBase64WithObserver(makeBody(), observer); err != nil {
		t.Fatalf("convert failed: %v", err)
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected cache hit to avoid second network call, got=%d", got)
	}
	mu.Lock()
	secondHit := fromCacheByURL[rawURL]
	mu.Unlock()
	if !secondHit {
		t.Fatalf("expected second convert to hit cache, got fromCache=false")
	}
}

func TestFetchImageUrlAsInlineData_BackgroundBridge_ReusesInFlightAcrossRetries(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	cache, err := newInlineDataURLDiskCache(t.TempDir(), time.Hour, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	release := make(chan struct{})
	rt := &blockingTransport{
		contentType: "image/jpeg",
		body:        []byte("slow-image-bytes"),
		releaseCh:   release,
	}
	fetcher, err := newInlineDataBackgroundFetcher(500*time.Millisecond, 16)
	if err != nil {
		t.Fatalf("background fetcher init failed: %v", err)
	}

	app := &App{
		Config: Config{
			ImageFetchTimeout:                        40 * time.Millisecond,
			InlineDataURLBackgroundFetchWaitTimeout:  40 * time.Millisecond,
			InlineDataURLBackgroundFetchTotalTimeout: 500 * time.Millisecond,
		},
		ImageFetchClient:               &http.Client{Timeout: 40 * time.Millisecond, Transport: rt},
		ImageFetchBackgroundClient:     &http.Client{Timeout: 500 * time.Millisecond, Transport: rt},
		InlineDataURLCache:             cache,
		InlineDataURLBackgroundFetcher: fetcher,
	}

	_, _, _, err = app.fetchImageUrlAsInlineData(rawURL)
	if err == nil {
		t.Fatalf("expected first fetch to timeout waiting for background download, got nil")
	}
	var waitErr *inlineDataBackgroundWaitTimeoutError
	if !errors.As(err, &waitErr) {
		t.Fatalf("expected background wait timeout error, got: %v", err)
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected exactly 1 upstream fetch started, got=%d", got)
	}

	type result struct {
		fromCache bool
		err       error
	}
	done := make(chan result, 1)
	go func() {
		_, _, fromCache, err := app.fetchImageUrlAsInlineData(rawURL)
		done <- result{fromCache: fromCache, err: err}
	}()

	time.Sleep(20 * time.Millisecond)
	close(release)

	select {
	case res := <-done:
		if res.err != nil {
			t.Fatalf("second fetch failed: %v", res.err)
		}
		if res.fromCache {
			t.Fatalf("expected second fetch to reuse in-flight result (not disk hit), got fromCache=true")
		}
	case <-time.After(1 * time.Second):
		t.Fatalf("second fetch did not complete in time")
	}

	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected retry to reuse same in-flight download, got upstream calls=%d", got)
	}

	_, _, fromCache3, err := app.fetchImageUrlAsInlineData(rawURL)
	if err != nil {
		t.Fatalf("third fetch failed: %v", err)
	}
	if !fromCache3 {
		t.Fatalf("expected third fetch to hit disk cache, got fromCache=false")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected disk cache hit to avoid extra upstream fetches, got=%d", got)
	}
}

func TestFetchImageUrlAsInlineData_BackgroundBridge_RespectsTotalTimeout(t *testing.T) {
	rawURL := "https://miratoon.oss-cn-hangzhou.aliyuncs.com/SHOT_VALUE_IMAGE/20260110/0d5f38a5c7364b4c846971cbcb017614.jpg"
	cache, err := newInlineDataURLDiskCache(t.TempDir(), time.Hour, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	neverRelease := make(chan struct{})
	rt := &blockingTransport{
		contentType: "image/jpeg",
		body:        []byte("never-released"),
		releaseCh:   neverRelease,
	}
	fetcher, err := newInlineDataBackgroundFetcher(120*time.Millisecond, 16)
	if err != nil {
		t.Fatalf("background fetcher init failed: %v", err)
	}

	app := &App{
		Config: Config{
			ImageFetchTimeout:                        40 * time.Millisecond,
			InlineDataURLBackgroundFetchWaitTimeout:  40 * time.Millisecond,
			InlineDataURLBackgroundFetchTotalTimeout: 120 * time.Millisecond,
		},
		ImageFetchClient:               &http.Client{Timeout: 40 * time.Millisecond, Transport: rt},
		ImageFetchBackgroundClient:     &http.Client{Timeout: 120 * time.Millisecond, Transport: rt},
		InlineDataURLCache:             cache,
		InlineDataURLBackgroundFetcher: fetcher,
	}

	_, _, _, err = app.fetchImageUrlAsInlineData(rawURL)
	if err == nil {
		t.Fatalf("expected first fetch to timeout waiting, got nil")
	}
	if got := rt.getCallCount(); got != 1 {
		t.Fatalf("expected first attempt to start one upstream fetch, got=%d", got)
	}

	time.Sleep(180 * time.Millisecond)

	_, _, _, err = app.fetchImageUrlAsInlineData(rawURL)
	if err == nil {
		t.Fatalf("expected second fetch to still fail waiting, got nil")
	}
	if got := rt.getCallCount(); got != 2 {
		t.Fatalf("expected a new upstream fetch after total timeout, got=%d", got)
	}
}

func TestHandleNonStreamResponse_DropsSmallerInlineImageBase64(t *testing.T) {
	smallB64 := mustEncodePatternPNGBase64(t, 32)
	largeB64 := mustEncodePatternPNGBase64(t, 128)
	upstreamBody := makeInlineDataResponseBody(
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

	app.handleNonStreamResponse(rr, resp, "", req, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rr.Code, rr.Body.String())
	}
	parts := extractCandidateParts(t, rr.Body.Bytes())
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part after normalization, got=%d body=%s", len(parts), rr.Body.String())
	}
	if got := extractInlineDataField(t, parts[0], "data"); got != largeB64 {
		t.Fatalf("expected larger image to be kept, got=%q want=%q", got, largeB64)
	}
}

func TestHandleNonStreamResponse_OutputURL_DropsSmallerInlineImageBeforeUpload(t *testing.T) {
	smallB64 := mustEncodePatternPNGBase64(t, 32)
	largeB64 := mustEncodePatternPNGBase64(t, 128)
	upstreamBody := makeInlineDataResponseBody(
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

	uploadTransport := &uploadSuccessTransport{}
	app := &App{
		Config:       Config{},
		UploadClient: &http.Client{Transport: uploadTransport},
	}
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(upstreamBody)),
	}
	req := httptest.NewRequest(http.MethodPost, "http://localhost/v1beta/models/test:generateContent?output=url", nil)
	rr := httptest.NewRecorder()

	app.handleNonStreamResponse(rr, resp, "url", req, nil)

	if rr.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%s", rr.Code, rr.Body.String())
	}
	if got := uploadTransport.getCallCount(); got != 1 {
		t.Fatalf("expected only one upload after dropping smaller image, got=%d", got)
	}
	parts := extractCandidateParts(t, rr.Body.Bytes())
	if len(parts) != 1 {
		t.Fatalf("expected 1 image part after url conversion, got=%d body=%s", len(parts), rr.Body.String())
	}
	if got := extractInlineDataField(t, parts[0], "data"); got != "https://img.example.com/upload-1.jpg" {
		t.Fatalf("unexpected uploaded url: %q", got)
	}
}

func TestHandleNonStreamResponse_PreservesTextPartWhileDroppingSmallerInlineImages(t *testing.T) {
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

	app.handleNonStreamResponse(rr, resp, "", req, nil)

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
		t.Fatalf("expected larger image to be kept, got=%q want=%q", got, largeB64)
	}
}

func TestSSEScannerBufPool_Reuse(t *testing.T) {
	// Get a buffer, capture its pointer, return it, get again — should be same backing array.
	p1 := sseScannerBufPool.Get().(*[]byte)
	addr1 := &(*p1)[0]
	sseScannerBufPool.Put(p1)

	p2 := sseScannerBufPool.Get().(*[]byte)
	addr2 := &(*p2)[0]
	sseScannerBufPool.Put(p2)

	if addr1 != addr2 {
		t.Log("sync.Pool did not reuse buffer (GC may have collected it — acceptable under load)")
		return
	}
	if len(*p1) != MaxSSEScanTokenBytes {
		t.Fatalf("expected buf len=%d, got=%d", MaxSSEScanTokenBytes, len(*p1))
	}
}
