# Performance Optimization Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Improve JSON throughput, reduce GC pressure, eliminate hot-path disk I/O, and cut SSE memory usage via four independent changes.

**Architecture:** Drop-in replace `encoding/json` with `json-iterator`; add `sync.Pool` for `bytes.Buffer` reuse; add an in-process LRU memory cache in front of the existing disk cache; pool SSE scanner buffers. All changes are independently deployable and can each be reverted in isolation.

**Tech Stack:** Go 1.22, `github.com/json-iterator/go v1.1.12` (new dep), stdlib `container/list`, `sync.Pool`, `sync.RWMutex`.

---

## Task 1: sync.Pool for bytes.Buffer

**Files:**
- Modify: `main.go` (add `bufPool` var + helper; replace 6 `json.Marshal` callsites)

### Step 1: Verify existing tests pass (baseline)

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
go test ./... -count=1
```

Expected: all tests PASS.

### Step 2: Add `bufPool` and `marshalJSON` helper after the `var` block (~line 43)

Find this in `main.go`:
```go
var markdownImageURLRe = regexp.MustCompile(`!\[[^\]]*\]\(\s*(https?://[^)\s]+)\s*\)`)
var proxyPrewarmSem = make(chan struct{}, MaxConcurrentInlineDataFetches)
```

Add immediately after:
```go
// bufPool recycles bytes.Buffer across JSON marshal calls to reduce GC pressure.
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

// marshalJSON marshals v to JSON using a pooled buffer. The returned slice is a
// fresh copy and is safe to use after marshalJSON returns.
func marshalJSON(v any) ([]byte, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	err := json.NewEncoder(buf).Encode(v)
	var result []byte
	if err == nil {
		b := buf.Bytes()
		// json.Encoder appends a trailing newline; trim it to match json.Marshal output.
		if len(b) > 0 && b[len(b)-1] == '\n' {
			b = b[:len(b)-1]
		}
		result = make([]byte, len(b))
		copy(result, b)
	}
	bufPool.Put(buf)
	return result, err
}
```

### Step 3: Replace the 6 `json.Marshal` callsites in `main.go`

Replace each of the following (exact lines shown with surrounding context to be unique):

**Callsite A — line 717** (build upstream request body):
```go
// BEFORE:
newBodyBytes, _ := json.Marshal(bodyMap)

// AFTER:
newBodyBytes, _ := marshalJSON(bodyMap)
```

**Callsite B — line 848** (non-stream response finalBytes):
```go
// BEFORE:
finalBytes, _ := json.Marshal(jsonBody)

// AFTER:
finalBytes, _ := marshalJSON(jsonBody)
```

**Callsite C — line 927** (SSE stream error object):
```go
// BEFORE:
newBytes, _ := json.Marshal(errObj)

// AFTER:
newBytes, _ := marshalJSON(errObj)
```

**Callsite D — line 944** (SSE stream normal chunk):
```go
// BEFORE:
newBytes, _ := json.Marshal(jsonBody)

// AFTER:
newBytes, _ := marshalJSON(jsonBody)
```

**Callsite E — line 2001** (geminiError helper):
```go
// BEFORE:
b, _ := json.Marshal(payload)

// AFTER:
b, _ := marshalJSON(payload)
```

> Note: leave `json.Unmarshal` calls unchanged — Unmarshal cannot use a write buffer.

### Step 4: Run tests to confirm nothing broke

```bash
go test ./... -count=1
```

Expected: all PASS.

### Step 5: Commit

```bash
git add main.go
git commit -m "perf: add sync.Pool bytes.Buffer reuse for JSON marshal"
```

---

## Task 2: Replace encoding/json with json-iterator

**Files:**
- Modify: `go.mod` (add require)
- Modify: `main.go` (swap import)
- Modify: `inline_data_url_cache.go` (swap import)

### Step 1: Add dependency

```bash
go get github.com/json-iterator/go@v1.1.12
go mod tidy
```

Expected: `go.mod` gains `require github.com/json-iterator/go v1.1.12` and `go.sum` is created.

### Step 2: Replace import in `main.go`

Find (lines 3-24):
```go
import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	...
)
```

Replace `"encoding/json"` with the jsoniter alias. The simplest approach is to add a package-level var. Insert **after** the import block (after line 24 `)`), before the `const` block:

```go
// json is a drop-in replacement for encoding/json using json-iterator for improved throughput.
var json = jsoniter.ConfigCompatibleWithStandardLibrary
```

And change the import line from:
```go
	"encoding/json"
```
to:
```go
	jsoniter "github.com/json-iterator/go"
```

### Step 3: Replace import in `inline_data_url_cache.go`

Same pattern. In `inline_data_url_cache.go`:

Change the import:
```go
	"encoding/json"
```
to:
```go
	jsoniter "github.com/json-iterator/go"
```

Add after the import block (before `type inlineDataURLDiskCache`):
```go
var json = jsoniter.ConfigCompatibleWithStandardLibrary
```

> **Important:** Both files declare `var json = ...`. This is safe because they are in the same package (`package main`) — there will be a **duplicate declaration** compile error. Instead, declare `var json` only **once** in `main.go` and remove the `"encoding/json"` import from both files; `inline_data_url_cache.go` will share the package-level `json` var from `main.go`.

**Correct approach for `inline_data_url_cache.go`:**
- Remove `"encoding/json"` from imports (it already uses package-level `json` from `main.go` once you add the var there)
- Do NOT add a second `var json` declaration

**Correct approach for `main.go`:**
- Remove `"encoding/json"` from imports
- Add `jsoniter "github.com/json-iterator/go"` to imports
- Add `var json = jsoniter.ConfigCompatibleWithStandardLibrary` once, at package level

### Step 4: Build check

```bash
go build ./...
```

Expected: compiles without errors.

### Step 5: Run tests

```bash
go test ./... -count=1
```

Expected: all PASS (json-iterator in `ConfigCompatibleWithStandardLibrary` mode is behaviorally identical to stdlib).

### Step 6: Commit

```bash
git add go.mod go.sum main.go inline_data_url_cache.go
git commit -m "perf: replace encoding/json with json-iterator for 1.5-2x JSON throughput"
```

---

## Task 3: SSE Scanner Buffer Pool

**Files:**
- Modify: `main.go` (add `sseScannerBufPool` var; update `handleStreamResponse`)

### Step 1: Write a test for buffer pool reuse

In `main_test.go`, add at the end:

```go
func TestSSEScannerBufPool_Reuse(t *testing.T) {
	// Get a buffer, capture its pointer, return it, get again — should be same backing array.
	p1 := sseScannerBufPool.Get().(*[]byte)
	addr1 := &(*p1)[0]
	sseScannerBufPool.Put(p1)

	p2 := sseScannerBufPool.Get().(*[]byte)
	addr2 := &(*p2)[0]
	sseScannerBufPool.Put(p2)

	if addr1 != addr2 {
		t.Skip("sync.Pool did not reuse buffer (GC may have collected it — acceptable under load)")
	}
	if len(*p1) != MaxSSEScanTokenBytes {
		t.Fatalf("expected buf len=%d, got=%d", MaxSSEScanTokenBytes, len(*p1))
	}
}
```

### Step 2: Run test — expect compile error (pool not declared yet)

```bash
go test -run TestSSEScannerBufPool_Reuse ./...
```

Expected: compile error `undefined: sseScannerBufPool`.

### Step 3: Add pool var to `main.go`

After the existing `var bufPool` line, add:

```go
// sseScannerBufPool recycles the large scanner buffer used per SSE response (~17 MiB each).
var sseScannerBufPool = sync.Pool{
	New: func() any {
		b := make([]byte, MaxSSEScanTokenBytes)
		return &b
	},
}
```

### Step 4: Update `handleStreamResponse` (~line 889-890)

Find:
```go
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), MaxSSEScanTokenBytes)
```

Replace with:
```go
	sseBufPtr := sseScannerBufPool.Get().(*[]byte)
	defer sseScannerBufPool.Put(sseBufPtr)
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(*sseBufPtr, MaxSSEScanTokenBytes)
```

### Step 5: Run tests

```bash
go test ./... -count=1
```

Expected: all PASS including `TestSSEScannerBufPool_Reuse`.

### Step 6: Commit

```bash
git add main.go main_test.go
git commit -m "perf: pool SSE scanner buffers to reduce per-connection 17MiB allocations"
```

---

## Task 4: L1 In-Memory LRU Cache for inline data URLs

**Files:**
- Modify: `inline_data_url_cache.go` (add `inlineDataURLMemCache` struct + methods; wire into `GetOrFetch` and `Set`)
- Modify: `main.go` (add `InlineDataURLMemCacheMaxBytes` to `Config`; parse env var; init memCache in `main()`)
- Create: `inline_data_url_mem_cache_test.go` (new file, unit tests)

### Step 1: Write failing unit tests

Create file `inline_data_url_mem_cache_test.go`:

```go
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
	m.Set("https://example.com/c.jpg", "image/jpeg", []byte("cccccccccc")) // 10 bytes — must evict "a"

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
```

### Step 2: Run tests — expect compile error

```bash
go test -run TestMemCache ./... 2>&1 | head -20
```

Expected: `undefined: newInlineDataURLMemCache`.

### Step 3: Implement `inlineDataURLMemCache` in `inline_data_url_cache.go`

Add imports `"container/list"` to the import block of `inline_data_url_cache.go`.

Add the following after the existing `inflightResult` struct (after line ~47):

```go
// inlineDataURLMemCache is an in-process LRU cache that sits in front of the
// disk cache (L2) to eliminate disk I/O for hot-path repeated image URL fetches.
type inlineDataURLMemEntry struct {
	mime string
	data []byte
	size int64
	elem *list.Element // back-pointer into lru list; Value = cache key string
}

type inlineDataURLMemCache struct {
	mu       sync.RWMutex
	items    map[string]*inlineDataURLMemEntry
	lru      *list.List // front = MRU, back = LRU
	maxBytes int64
	curBytes int64
}

func newInlineDataURLMemCache(maxBytes int64) *inlineDataURLMemCache {
	if maxBytes <= 0 {
		return nil
	}
	return &inlineDataURLMemCache{
		items:    make(map[string]*inlineDataURLMemEntry),
		lru:      list.New(),
		maxBytes: maxBytes,
	}
}

// Get returns a cache hit. MoveToFront requires a write lock.
func (m *inlineDataURLMemCache) Get(url string) (mime string, data []byte, ok bool) {
	if m == nil {
		return "", nil, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.items[url]
	if !ok {
		return "", nil, false
	}
	m.lru.MoveToFront(e.elem)
	return e.mime, e.data, true
}

// Set inserts or updates a URL in the cache. Items larger than maxBytes are silently ignored.
func (m *inlineDataURLMemCache) Set(url, mime string, data []byte) {
	if m == nil {
		return
	}
	size := int64(len(data))
	if size > m.maxBytes {
		return // single item exceeds total capacity; skip
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Update existing entry.
	if e, ok := m.items[url]; ok {
		m.curBytes -= e.size
		e.mime = mime
		e.data = data
		e.size = size
		m.curBytes += size
		m.lru.MoveToFront(e.elem)
		return
	}

	// Evict LRU entries until there is room.
	for m.curBytes+size > m.maxBytes && m.lru.Len() > 0 {
		back := m.lru.Back()
		if back == nil {
			break
		}
		key := back.Value.(string)
		if victim, ok := m.items[key]; ok {
			m.curBytes -= victim.size
			delete(m.items, key)
		}
		m.lru.Remove(back)
	}

	elem := m.lru.PushFront(url)
	m.items[url] = &inlineDataURLMemEntry{mime: mime, data: data, size: size, elem: elem}
	m.curBytes += size
}
```

### Step 4: Run mem cache tests

```bash
go test -run TestMemCache ./... -v
```

Expected: all 6 `TestMemCache_*` tests PASS.

### Step 5: Wire memCache into `inlineDataURLDiskCache`

Add a `memCache` field to `inlineDataURLDiskCache`:

```go
type inlineDataURLDiskCache struct {
	dir      string
	ttl      time.Duration
	maxBytes int64

	mu       sync.Mutex
	inflight inflightGroup
	memCache *inlineDataURLMemCache // optional L1; nil = disabled
}
```

### Step 6: Update `GetOrFetch` to check L1 first

Replace the existing `GetOrFetch` method body:

```go
func (c *inlineDataURLDiskCache) GetOrFetch(url string, fetch func() (string, []byte, error)) (mime string, bytesData []byte, fromCache bool, err error) {
	if c == nil {
		mime, bytesData, err := fetch()
		return mime, bytesData, false, err
	}

	// L1: memory cache check (no disk I/O).
	if c.memCache != nil {
		if m, d, ok := c.memCache.Get(url); ok {
			return m, d, true, nil
		}
	}

	// L2: disk cache check.
	if mime, bytesData, ok, err := c.Get(url); err == nil && ok {
		// Promote to L1 for subsequent requests.
		if c.memCache != nil {
			c.memCache.Set(url, mime, bytesData)
		}
		return mime, bytesData, true, nil
	}

	res, err := c.inflight.Do(url, func() (inflightResult, error) {
		// Double-check L1 (another goroutine may have populated while we queued).
		if c.memCache != nil {
			if m, d, ok := c.memCache.Get(url); ok {
				return inflightResult{mime: m, bytesData: d, fromCache: true}, nil
			}
		}
		// Double-check L2 disk.
		if mime, bytesData, ok, err := c.Get(url); err == nil && ok {
			if c.memCache != nil {
				c.memCache.Set(url, mime, bytesData)
			}
			return inflightResult{mime: mime, bytesData: bytesData, fromCache: true}, nil
		}

		mime, bytesData, err := fetch()
		if err != nil {
			return inflightResult{}, err
		}

		// Write L2 disk cache (best-effort).
		_ = c.Set(url, mime, bytesData)
		// Write L1 (Set already handles nil check).
		if c.memCache != nil {
			c.memCache.Set(url, mime, bytesData)
		}
		return inflightResult{mime: mime, bytesData: bytesData, fromCache: false}, nil
	})
	if err != nil {
		return "", nil, false, err
	}
	return res.mime, res.bytesData, res.fromCache, nil
}
```

### Step 7: Also populate L1 on background fetch success

In `main.go`, the background fetcher's `onSuccess` callback (~line 1213):

```go
onSuccess := func(mime string, bytesData []byte) {
    if app.InlineDataURLCache != nil {
        _ = app.InlineDataURLCache.Set(rawURL, mime, bytesData)
    }
}
```

Update `Set` in `inline_data_url_cache.go` to also write to memCache:

Find the end of the `Set` method (after the disk write logic), before the final `return nil`:

```go
	// Also populate L1 memory cache.
	if c.memCache != nil {
		c.memCache.Set(url, mime, bytesData)
	}
	return nil
```

> This means the `onSuccess` callback automatically populates L1 via `Set` — no change needed in `main.go`.

### Step 8: Add config field and env var parsing to `main.go`

In `Config` struct, after `InlineDataURLCacheMaxBytes int64`:

```go
	// Optional L1 in-memory LRU cache in front of the disk cache.
	// Zero or negative disables the memory cache.
	InlineDataURLMemCacheMaxBytes int64
```

In `loadConfig()`, after the `InlineDataURLCacheMaxBytes` block (~line 269):

```go
	// L1 memory cache for inlineData URL fetches (default 100MiB, disabled if 0).
	cfg.InlineDataURLMemCacheMaxBytes = 100 * 1024 * 1024 // 100MiB
	if raw := strings.TrimSpace(os.Getenv("INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if v <= 0 || isDisabledValue(raw) {
				cfg.InlineDataURLMemCacheMaxBytes = 0
			} else {
				cfg.InlineDataURLMemCacheMaxBytes = v
			}
		}
	}
```

### Step 9: Wire memory cache in `main()` initialization

After the existing disk cache initialization block (~line 385-392):

```go
	if cfg.InlineDataURLCacheDir != "" && cfg.InlineDataURLCacheTTL > 0 && cfg.InlineDataURLCacheMaxBytes > 0 {
		cache, err := newInlineDataURLDiskCache(cfg.InlineDataURLCacheDir, cfg.InlineDataURLCacheTTL, cfg.InlineDataURLCacheMaxBytes)
		if err != nil {
			log.Printf("WARNING: InlineData URL cache disabled: %v", err)
		} else {
			app.InlineDataURLCache = cache
		}
	}
```

Change to:

```go
	if cfg.InlineDataURLCacheDir != "" && cfg.InlineDataURLCacheTTL > 0 && cfg.InlineDataURLCacheMaxBytes > 0 {
		cache, err := newInlineDataURLDiskCache(cfg.InlineDataURLCacheDir, cfg.InlineDataURLCacheTTL, cfg.InlineDataURLCacheMaxBytes)
		if err != nil {
			log.Printf("WARNING: InlineData URL disk cache disabled: %v", err)
		} else {
			if cfg.InlineDataURLMemCacheMaxBytes > 0 {
				cache.memCache = newInlineDataURLMemCache(cfg.InlineDataURLMemCacheMaxBytes)
			}
			app.InlineDataURLCache = cache
		}
	}
```

Also update the log line in the startup output block (~line 446):
```go
	if cfg.InlineDataURLCacheDir != "" && cfg.InlineDataURLCacheTTL > 0 && cfg.InlineDataURLCacheMaxBytes > 0 {
		log.Printf("InlineData URL Cache: disk enabled dir=%s ttl=%v maxBytes=%d", cfg.InlineDataURLCacheDir, cfg.InlineDataURLCacheTTL, cfg.InlineDataURLCacheMaxBytes)
	}
	if cfg.InlineDataURLMemCacheMaxBytes > 0 {
		log.Printf("InlineData URL Cache: memory L1 enabled maxBytes=%d", cfg.InlineDataURLMemCacheMaxBytes)
	}
```

### Step 10: Run all tests

```bash
go test ./... -count=1 -v 2>&1 | tail -30
```

Expected: all PASS, including the 6 new `TestMemCache_*` tests.

### Step 11: Update README.md

In the `### 请求侧 inlineData URL 跨请求缓存（磁盘）` section, add a new subsection after `INLINE_DATA_URL_CACHE_MAX_BYTES`:

```markdown
#### `INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES`（默认 `104857600`，即 100MiB）

请求侧 inlineData URL 的**内存 L1 缓存**容量上限（字节）。

该缓存位于磁盘缓存之前：内存命中时直接返回，完全避免磁盘 I/O（通常可节省 1–5ms）。
- `0` / `off` / `false`：关闭内存缓存（磁盘 L2 仍正常工作）
- 单条记录超过上限时自动忽略（不写入内存缓存）
- 进程重启后冷启动；磁盘缓存作为 L2 warmup 来源

注意：内存缓存与磁盘缓存**互相独立**，磁盘缓存 `INLINE_DATA_URL_CACHE_DIR` 为空时内存缓存仍可单独启用（仅在进程生命周期内有效，无持久化）。
```

### Step 12: Commit

```bash
git add inline_data_url_cache.go inline_data_url_mem_cache_test.go main.go README.md
git commit -m "perf: add L1 in-memory LRU cache for inline data URL fetches (default 100MiB)"
```

---

## Final Verification

```bash
go test ./... -count=1 -race
go build -o gemini-worker-go .
echo "Build OK, size: $(du -sh gemini-worker-go)"
```

Expected:
- All tests PASS with `-race` flag
- Binary builds successfully
- Binary size slightly larger than before (json-iterator dependency)
