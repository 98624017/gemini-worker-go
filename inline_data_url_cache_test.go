package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestInlineDataURLDiskCache_RefreshExpiryOnHit_DoesNotOverwriteNewerMeta(t *testing.T) {
	cache, err := newInlineDataURLDiskCache(t.TempDir(), time.Second, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	rawURL := "https://example.com/test.jpg"
	oldBytes := []byte("old-bytes")
	newBytes := []byte("new-image-bytes")

	if err := cache.Set(rawURL, "image/jpeg", oldBytes); err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	key := inlineDataURLCacheKey(rawURL)
	metaPath, _ := cache.pathsForKey(key)

	staleMetaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read seed meta failed: %v", err)
	}
	var staleMeta inlineDataURLDiskCacheMeta
	if err := json.Unmarshal(staleMetaBytes, &staleMeta); err != nil {
		t.Fatalf("unmarshal seed meta failed: %v", err)
	}

	if err := cache.Set(rawURL, "image/webp", newBytes); err != nil {
		t.Fatalf("overwrite cache failed: %v", err)
	}

	// Simulate a stale copy captured by Get() before a concurrent Set() lands.
	staleMeta.ExpiresAtUnixMs = time.Now().Add(-time.Second).UnixMilli()
	refreshAt := time.Now().Add(500 * time.Millisecond)
	cache.refreshExpiryOnHit(metaPath, &staleMeta, refreshAt)

	refreshedMetaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read refreshed meta failed: %v", err)
	}
	var refreshedMeta inlineDataURLDiskCacheMeta
	if err := json.Unmarshal(refreshedMetaBytes, &refreshedMeta); err != nil {
		t.Fatalf("unmarshal refreshed meta failed: %v", err)
	}

	if refreshedMeta.MimeType != "image/webp" {
		t.Fatalf("expected mimeType to keep newer Set value, got=%q", refreshedMeta.MimeType)
	}
	if refreshedMeta.SizeBytes != int64(len(newBytes)) {
		t.Fatalf("expected sizeBytes=%d, got=%d", len(newBytes), refreshedMeta.SizeBytes)
	}

	expectedExpiry := refreshAt.Add(cache.ttl).UnixMilli()
	if refreshedMeta.ExpiresAtUnixMs < expectedExpiry {
		t.Fatalf("expected expiresAtUnixMs >= %d, got=%d", expectedExpiry, refreshedMeta.ExpiresAtUnixMs)
	}
}

func TestInlineDataURLDiskCache_RefreshExpiryOnHit_DoesNotMutateCallerSnapshotMimeOrSize(t *testing.T) {
	cache, err := newInlineDataURLDiskCache(t.TempDir(), time.Second, 64<<20)
	if err != nil {
		t.Fatalf("cache init failed: %v", err)
	}

	rawURL := "https://example.com/test.jpg"
	oldBytes := []byte("old-bytes")
	newBytes := []byte("new-image-bytes")

	if err := cache.Set(rawURL, "image/jpeg", oldBytes); err != nil {
		t.Fatalf("seed cache failed: %v", err)
	}

	key := inlineDataURLCacheKey(rawURL)
	metaPath, _ := cache.pathsForKey(key)

	staleMetaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read seed meta failed: %v", err)
	}
	var staleMeta inlineDataURLDiskCacheMeta
	if err := json.Unmarshal(staleMetaBytes, &staleMeta); err != nil {
		t.Fatalf("unmarshal seed meta failed: %v", err)
	}

	if err := cache.Set(rawURL, "image/webp", newBytes); err != nil {
		t.Fatalf("overwrite cache failed: %v", err)
	}

	staleMeta.ExpiresAtUnixMs = time.Now().Add(-time.Second).UnixMilli()
	refreshAt := time.Now().Add(500 * time.Millisecond)
	cache.refreshExpiryOnHit(metaPath, &staleMeta, refreshAt)

	if staleMeta.MimeType != "image/jpeg" {
		t.Fatalf("expected caller mimeType snapshot to stay image/jpeg, got=%q", staleMeta.MimeType)
	}
	if staleMeta.SizeBytes != int64(len(oldBytes)) {
		t.Fatalf("expected caller sizeBytes snapshot=%d, got=%d", len(oldBytes), staleMeta.SizeBytes)
	}
	expectedExpiry := refreshAt.Add(cache.ttl).UnixMilli()
	if staleMeta.ExpiresAtUnixMs < expectedExpiry {
		t.Fatalf("expected caller expiresAtUnixMs >= %d, got=%d", expectedExpiry, staleMeta.ExpiresAtUnixMs)
	}
}
