package main

import (
	"testing"
)

func TestMemCache_GetMiss(t *testing.T) {
	m := newInlineDataURLMemCache(1 << 20) // 1MiB
	_, _, ok := m.Get("https://example.com/img.jpg")
	if ok {
		t.Fatal("expected miss on empty cache")
	}
}

func TestMemCache_SetGet(t *testing.T) {
	m := newInlineDataURLMemCache(1 << 20)
	data := []byte("fake-image-bytes")
	m.Set("https://example.com/img.jpg", "image/jpeg", data)

	mime, got, ok := m.Get("https://example.com/img.jpg")
	if !ok {
		t.Fatal("expected hit after Set")
	}
	if mime != "image/jpeg" {
		t.Fatalf("expected mime=image/jpeg, got=%q", mime)
	}
	if string(got) != string(data) {
		t.Fatalf("expected data=%q, got=%q", data, got)
	}
}

func TestMemCache_EvictsLRUWhenFull(t *testing.T) {
	// Allow only 20 bytes; each entry is 10 bytes.
	m := newInlineDataURLMemCache(20)
	m.Set("https://example.com/a.jpg", "image/jpeg", []byte("aaaaaaaaaa")) // 10 bytes — fills half
	m.Set("https://example.com/b.jpg", "image/jpeg", []byte("bbbbbbbbbb")) // 10 bytes — fills to max
	m.Set("https://example.com/c.jpg", "image/jpeg", []byte("cccccccccc")) // 10 bytes — must evict oldest

	_, _, okA := m.Get("https://example.com/a.jpg")
	_, _, okB := m.Get("https://example.com/b.jpg")
	_, _, okC := m.Get("https://example.com/c.jpg")

	if okA {
		t.Fatal("expected 'a' to be evicted (LRU)")
	}
	if !okB {
		t.Fatal("expected 'b' to remain")
	}
	if !okC {
		t.Fatal("expected 'c' (newest) to remain")
	}
}

func TestMemCache_UpdateExistingKey(t *testing.T) {
	m := newInlineDataURLMemCache(1 << 20)
	m.Set("https://example.com/img.jpg", "image/jpeg", []byte("old"))
	m.Set("https://example.com/img.jpg", "image/webp", []byte("new-data"))

	mime, data, ok := m.Get("https://example.com/img.jpg")
	if !ok {
		t.Fatal("expected hit")
	}
	if mime != "image/webp" {
		t.Fatalf("expected mime=image/webp, got=%q", mime)
	}
	if string(data) != "new-data" {
		t.Fatalf("expected data=new-data, got=%q", data)
	}
}

func TestMemCache_NilSafe(t *testing.T) {
	var m *inlineDataURLMemCache
	m.Set("https://example.com/img.jpg", "image/jpeg", []byte("data")) // must not panic
	_, _, ok := m.Get("https://example.com/img.jpg")
	if ok {
		t.Fatal("nil cache should always miss")
	}
}

func TestMemCache_ItemLargerThanMaxIsIgnored(t *testing.T) {
	m := newInlineDataURLMemCache(5) // only 5 bytes max
	m.Set("https://example.com/img.jpg", "image/jpeg", []byte("more-than-5-bytes"))
	_, _, ok := m.Get("https://example.com/img.jpg")
	if ok {
		t.Fatal("item larger than maxBytes should be ignored")
	}
}

func TestMemCache_ConcurrentReadWrite(t *testing.T) {
	m := newInlineDataURLMemCache(10 << 20) // 10MiB
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func(i int) {
			key := "https://example.com/img-" + string(rune('a'+i)) + ".jpg"
			m.Set(key, "image/jpeg", []byte("data"))
			m.Get(key)
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestMemCache_UpdateLargerEvictsOthers(t *testing.T) {
	// maxBytes = 20; insert A(10) + B(10) = full.
	// Then update A to 15 bytes: curBytes would become 25 > 20, so B must be evicted.
	m := newInlineDataURLMemCache(20)
	m.Set("https://example.com/a.jpg", "image/jpeg", []byte("aaaaaaaaaa")) // 10 bytes
	m.Set("https://example.com/b.jpg", "image/jpeg", []byte("bbbbbbbbbb")) // 10 bytes — full

	// Update A with larger data (15 bytes); eviction loop should remove B to stay within limit.
	m.Set("https://example.com/a.jpg", "image/jpeg", []byte("aaaaaaaaaaaaaaa")) // 15 bytes

	_, _, okA := m.Get("https://example.com/a.jpg")
	_, _, okB := m.Get("https://example.com/b.jpg")

	if !okA {
		t.Fatal("expected 'a' (updated) to remain in cache")
	}
	if okB {
		t.Fatal("expected 'b' to be evicted after 'a' was updated to a larger size")
	}

	m.mu.Lock()
	cur := m.curBytes
	max := m.maxBytes
	m.mu.Unlock()
	if cur > max {
		t.Fatalf("curBytes=%d exceeded maxBytes=%d after update", cur, max)
	}
}
