package main

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type inlineDataURLDiskCache struct {
	dir      string
	ttl      time.Duration
	maxBytes int64

	mu       sync.Mutex
	inflight inflightGroup
	memCache *inlineDataURLMemCache // optional L1; nil = disabled
}

type inlineDataURLDiskCacheMeta struct {
	MimeType        string `json:"mimeType"`
	ExpiresAtUnixMs int64  `json:"expiresAtUnixMs"`
	SizeBytes       int64  `json:"sizeBytes"`
}

type inflightGroup struct {
	mu sync.Mutex
	m  map[string]*inflightCall
}

type inflightCall struct {
	wg  sync.WaitGroup
	res inflightResult
	err error
}

type inflightResult struct {
	mime      string
	bytesData []byte
	fromCache bool
}

// inlineDataURLMemCache is an in-process LRU cache (L1) that sits in front of
// the disk cache (L2) to eliminate disk I/O for hot-path repeated URL fetches.
type inlineDataURLMemEntry struct {
	mime string
	data []byte
	size int64
	elem *list.Element // back-pointer into lru list; Value = URL string (cache key)
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

// Get returns a cached entry and moves it to MRU position. Requires a write lock for MoveToFront.
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

// Set inserts or updates an entry. Items larger than maxBytes are silently ignored.
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

	// Evict LRU entries until there is room for the new entry.
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

func (g *inflightGroup) Do(key string, fn func() (inflightResult, error)) (inflightResult, error) {
	g.mu.Lock()
	if g.m == nil {
		g.m = make(map[string]*inflightCall)
	}
	if c, ok := g.m[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.res, c.err
	}

	c := &inflightCall{}
	c.wg.Add(1)
	g.m[key] = c
	g.mu.Unlock()

	defer func() {
		// Ensure waiters never hang even if fn panics.
		if r := recover(); r != nil {
			c.err = fmt.Errorf("inflight panic: %v", r)
		}

		// Keep the key visible until the result is published and waiters are released.
		c.wg.Done()

		g.mu.Lock()
		delete(g.m, key)
		g.mu.Unlock()
	}()

	c.res, c.err = fn()

	return c.res, c.err
}

func newInlineDataURLDiskCache(dir string, ttl time.Duration, maxBytes int64) (*inlineDataURLDiskCache, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, errors.New("INLINE_DATA_URL_CACHE_DIR is empty")
	}
	if ttl <= 0 {
		return nil, errors.New("INLINE_DATA_URL_CACHE_TTL_MS must be > 0")
	}
	if maxBytes <= 0 {
		return nil, errors.New("INLINE_DATA_URL_CACHE_MAX_BYTES must be > 0")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &inlineDataURLDiskCache{
		dir:      dir,
		ttl:      ttl,
		maxBytes: maxBytes,
	}, nil
}

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
		// Double-check L1 (another goroutine may have populated while we waited).
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

		// Write L2 disk cache (best-effort). L1 is written inside Set.
		_ = c.Set(url, mime, bytesData)
		return inflightResult{mime: mime, bytesData: bytesData, fromCache: false}, nil
	})
	if err != nil {
		return "", nil, false, err
	}
	return res.mime, res.bytesData, res.fromCache, nil
}

func (c *inlineDataURLDiskCache) Get(url string) (mime string, bytesData []byte, ok bool, err error) {
	if c == nil || strings.TrimSpace(url) == "" {
		return "", nil, false, nil
	}

	key := inlineDataURLCacheKey(url)
	metaPath, dataPath := c.pathsForKey(key)

	metaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, false, nil
		}
		return "", nil, false, err
	}

	var meta inlineDataURLDiskCacheMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		// Corrupted meta: best-effort cleanup and treat as miss.
		_ = os.Remove(metaPath)
		_ = os.Remove(dataPath)
		return "", nil, false, nil
	}

	now := time.Now()
	if meta.ExpiresAtUnixMs <= 0 {
		_ = os.Remove(metaPath)
		_ = os.Remove(dataPath)
		return "", nil, false, nil
	}
	if meta.ExpiresAtUnixMs > 0 && now.UnixMilli() > meta.ExpiresAtUnixMs {
		_ = os.Remove(metaPath)
		_ = os.Remove(dataPath)
		return "", nil, false, nil
	}

	// Never serve/cache entries larger than our inlineData max.
	if meta.SizeBytes <= 0 || meta.SizeBytes > MaxImageBytes {
		_ = os.Remove(metaPath)
		_ = os.Remove(dataPath)
		return "", nil, false, nil
	}

	bytesData, err = os.ReadFile(dataPath)
	if err != nil {
		// Orphan meta: cleanup and treat as miss.
		_ = os.Remove(metaPath)
		return "", nil, false, nil
	}
	if int64(len(bytesData)) != meta.SizeBytes {
		_ = os.Remove(metaPath)
		_ = os.Remove(dataPath)
		return "", nil, false, nil
	}

	// Touch mtime so eviction approximates LRU.
	_ = os.Chtimes(dataPath, now, now)

	// Sliding TTL: refresh expiry window on cache hit.
	c.refreshExpiryOnHit(metaPath, &meta, now)
	return meta.MimeType, bytesData, true, nil
}

func (c *inlineDataURLDiskCache) refreshExpiryOnHit(metaPath string, meta *inlineDataURLDiskCacheMeta, now time.Time) {
	if c == nil || meta == nil || strings.TrimSpace(metaPath) == "" {
		return
	}
	if c.ttl <= 0 {
		return
	}

	nextExpiry := now.Add(c.ttl).UnixMilli()
	if nextExpiry <= meta.ExpiresAtUnixMs {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	updated := *meta
	latestMetaBytes, err := os.ReadFile(metaPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		return
	}
	if err := json.Unmarshal(latestMetaBytes, &updated); err != nil {
		return
	}
	if nextExpiry <= updated.ExpiresAtUnixMs {
		if updated.ExpiresAtUnixMs > meta.ExpiresAtUnixMs {
			meta.ExpiresAtUnixMs = updated.ExpiresAtUnixMs
		}
		return
	}

	updated.ExpiresAtUnixMs = nextExpiry
	metaBytes, err := json.Marshal(updated)
	if err != nil {
		return
	}

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return
	}
	tmpMetaPath, err := writeTempFile(c.dir, "refresh-meta-", metaBytes)
	if err != nil {
		return
	}
	if err := replaceFile(tmpMetaPath, metaPath); err != nil {
		_ = os.Remove(tmpMetaPath)
		return
	}
	meta.ExpiresAtUnixMs = updated.ExpiresAtUnixMs
}

func (c *inlineDataURLDiskCache) Set(url string, mime string, bytesData []byte) error {
	if c == nil || strings.TrimSpace(url) == "" || len(bytesData) == 0 {
		return nil
	}
	if int64(len(bytesData)) > MaxImageBytes {
		return nil
	}

	key := inlineDataURLCacheKey(url)
	metaPath, dataPath := c.pathsForKey(key)

	meta := inlineDataURLDiskCacheMeta{
		MimeType:        mime,
		ExpiresAtUnixMs: time.Now().Add(c.ttl).UnixMilli(),
		SizeBytes:       int64(len(bytesData)),
	}
	metaBytes, err := json.Marshal(meta)
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}

	tmpDataPath, err := writeTempFile(c.dir, key+".data.tmp-", bytesData)
	if err != nil {
		return err
	}
	if err := replaceFile(tmpDataPath, dataPath); err != nil {
		_ = os.Remove(tmpDataPath)
		return err
	}

	tmpMetaPath, err := writeTempFile(c.dir, key+".meta.tmp-", metaBytes)
	if err != nil {
		_ = os.Remove(dataPath)
		return err
	}
	if err := replaceFile(tmpMetaPath, metaPath); err != nil {
		_ = os.Remove(tmpMetaPath)
		_ = os.Remove(dataPath)
		return err
	}

	// Also populate L1 memory cache so background-fetch onSuccess hits L1 too.
	if c.memCache != nil {
		c.memCache.Set(url, mime, bytesData)
	}

	return c.pruneLocked()
}

func (c *inlineDataURLDiskCache) pruneLocked() error {
	type item struct {
		dataPath string
		metaPath string
		size     int64
		modTime  time.Time
	}

	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return err
	}

	var items []item
	var total int64

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".data") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		size := info.Size()
		if size <= 0 {
			continue
		}

		total += size
		base := strings.TrimSuffix(name, ".data")
		items = append(items, item{
			dataPath: filepath.Join(c.dir, name),
			metaPath: filepath.Join(c.dir, base+".meta.json"),
			size:     size,
			modTime:  info.ModTime(),
		})
	}

	if total <= c.maxBytes {
		return nil
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].modTime.Before(items[j].modTime)
	})

	for _, it := range items {
		if total <= c.maxBytes {
			break
		}
		if err := os.Remove(it.dataPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			// Best-effort: if we cannot delete this file (locked/permission), try next one.
			continue
		}
		_ = os.Remove(it.metaPath)
		total -= it.size
	}

	return nil
}

func (c *inlineDataURLDiskCache) pathsForKey(key string) (metaPath string, dataPath string) {
	return filepath.Join(c.dir, key+".meta.json"), filepath.Join(c.dir, key+".data")
}

func inlineDataURLCacheKey(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

func writeTempFile(dir string, pattern string, content []byte) (string, error) {
	f, err := os.CreateTemp(dir, pattern+"*")
	if err != nil {
		return "", err
	}
	path := f.Name()
	_, werr := f.Write(content)
	cerr := f.Close()
	if werr != nil {
		_ = os.Remove(path)
		return "", werr
	}
	if cerr != nil {
		_ = os.Remove(path)
		return "", cerr
	}
	return path, nil
}

func replaceFile(src string, dst string) error {
	if strings.TrimSpace(src) == "" || strings.TrimSpace(dst) == "" {
		return errors.New("invalid replace path")
	}

	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Windows does not allow renaming over existing files.
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}
