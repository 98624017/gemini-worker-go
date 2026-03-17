package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeJSONForAdminLog_OmitsBase64AndKeepsURL(t *testing.T) {
	raw := []byte(`{
  "contents": [{
    "parts": [{
      "inlineData": {
        "mimeType": "image/png",
        "data": "AAAABASE64SHOULDBEOMITTEDAAA"
      }
    },{
      "inlineData": {
        "mimeType": "image/png",
        "data": "https://example.com/a.png"
      }
    }]
  }]
}`)

	out, urls := sanitizeJSONForAdminLog(raw)
	if strings.Contains(out, "AAAABASE64SHOULDBEOMITTEDAAA") {
		t.Fatalf("expected base64 to be omitted, but found original content")
	}
	if !strings.Contains(out, "[base64 omitted") {
		t.Fatalf("expected base64 placeholder in output, got=%q", out)
	}
	if !strings.Contains(out, "https://example.com/a.png") {
		t.Fatalf("expected URL to be preserved, got=%q", out)
	}
	if len(urls) != 1 || urls[0] != "https://example.com/a.png" {
		t.Fatalf("unexpected urls: %#v", urls)
	}
}

func TestAdminRoutes_DisabledByDefault(t *testing.T) {
	app := &App{
		Config:    Config{AdminPassword: ""},
		AdminLogs: nil,
	}
	req := httptest.NewRequest("GET", "http://localhost/admin", nil)
	rr := httptest.NewRecorder()
	app.Handler(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when admin disabled, got=%d", rr.Code)
	}
}

func TestAdminRoutes_RequireBasicAuth(t *testing.T) {
	app := &App{
		Config:    Config{AdminPassword: "pw"},
		AdminLogs: newAdminLogBuffer(100),
	}
	app.AdminLogs.Add(adminLogEntry{Method: "POST", Path: "/v1beta/models/x:generateContent", StatusCode: 200})

	req := httptest.NewRequest("GET", "http://localhost/admin/api/logs", nil)
	rr := httptest.NewRecorder()
	app.Handler(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when missing auth, got=%d body=%q", rr.Code, rr.Body.String())
	}

	req2 := httptest.NewRequest("GET", "http://localhost/admin/api/logs", nil)
	req2.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("user:pw")))
	rr2 := httptest.NewRecorder()
	app.Handler(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid auth, got=%d body=%q", rr2.Code, rr2.Body.String())
	}
}
