package main

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"
)

type uploadInspectTransport struct {
	t             *testing.T
	contentLength int64
	getBodyNil    bool
	body          []byte
}

func (t *uploadInspectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.contentLength = req.ContentLength
	t.getBodyNil = req.GetBody == nil

	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.t.Fatalf("read request body failed: %v", err)
	}
	t.body = body

	responseBody := []byte(`{"success":true,"files":[{"url":"https://img.example.com/a.jpg"}]}`)
	if req.URL != nil && strings.Contains(req.URL.Host, "kefan.cn") {
		responseBody = []byte(`{"success":true,"data":"https://img.example.com/a.jpg"}`)
	}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(responseBody)),
		Request:    req,
	}
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

func TestUploadToUguu_UsesStreamingMultipartBody(t *testing.T) {
	transport := &uploadInspectTransport{t: t}
	app := &App{
		UploadClient: &http.Client{Transport: transport},
	}

	if _, err := app.uploadToUguu([]byte("hello-image"), "image/png"); err != nil {
		t.Fatalf("uploadToUguu returned error: %v", err)
	}

	if transport.contentLength != 0 {
		t.Fatalf("expected streaming multipart body with unknown size, got contentLength=%d", transport.contentLength)
	}
	if !transport.getBodyNil {
		t.Fatal("expected streaming request to have nil GetBody")
	}
	if !bytes.Contains(transport.body, []byte("hello-image")) {
		t.Fatal("expected multipart body to contain image bytes")
	}
}

func TestUploadToKefan_UsesStreamingMultipartBody(t *testing.T) {
	transport := &uploadInspectTransport{t: t}
	app := &App{
		UploadClient: &http.Client{Transport: transport},
	}

	if _, err := app.uploadToKefan([]byte("hello-image"), "image/png"); err != nil {
		t.Fatalf("uploadToKefan returned error: %v", err)
	}

	if transport.contentLength != 0 {
		t.Fatalf("expected streaming multipart body with unknown size, got contentLength=%d", transport.contentLength)
	}
	if !transport.getBodyNil {
		t.Fatal("expected streaming request to have nil GetBody")
	}
	if !bytes.Contains(transport.body, []byte("hello-image")) {
		t.Fatal("expected multipart body to contain image bytes")
	}
}
