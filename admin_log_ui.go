package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	adminMaxBodyBytesPerEntry = 64 * 1024
)

type adminLogEntry struct {
	ID         int64     `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Query      string    `json:"query"`
	RemoteAddr string    `json:"remoteAddr"`
	IsStream   bool      `json:"isStream"`
	OutputMode string    `json:"outputMode"`
	StatusCode int       `json:"statusCode"`
	DurationMs int64    `json:"durationMs"`

	RequestRaw               string   `json:"requestRaw"`
	RequestRawImages         []string `json:"requestRawImages"`
	RequestRawImageCacheHits []string `json:"requestRawImageCacheHits"`
	RequestUpstream          string   `json:"requestUpstream"`
	RequestUpstreamImgs      []string `json:"requestUpstreamImages"`
	ResponseDownstream       string   `json:"responseDownstream"`
	ResponseImages           []string `json:"responseImages"`
}

type adminLogBuffer struct {
	mu       sync.Mutex
	nextID   int64
	capacity int
	buf      []adminLogEntry
	full     bool
	nextIdx  int
}

// adminStats tracks aggregate request counters since process start.
// All fields are accessed atomically; no lock needed.
type adminStats struct {
	totalRequests   atomic.Int64
	errorRequests   atomic.Int64
	totalDurationMs atomic.Int64
	cacheHits       atomic.Int64
}

type limitedCaptureWriter struct {
	limit int
	buf   []byte
}

func (w *limitedCaptureWriter) Write(p []byte) (int, error) {
	if w == nil {
		return len(p), nil
	}
	limit := w.limit
	if limit <= 0 {
		limit = adminMaxBodyBytesPerEntry
	}
	if len(w.buf) < limit && len(p) > 0 {
		remain := limit - len(w.buf)
		if remain > 0 {
			if len(p) > remain {
				w.buf = append(w.buf, p[:remain]...)
			} else {
				w.buf = append(w.buf, p...)
			}
		}
	}
	return len(p), nil
}

func (w *limitedCaptureWriter) Bytes() []byte {
	if w == nil {
		return nil
	}
	return w.buf
}

func newAdminLogBuffer(capacity int) *adminLogBuffer {
	if capacity <= 0 {
		capacity = 100
	}
	return &adminLogBuffer{
		capacity: capacity,
		buf:      make([]adminLogEntry, capacity),
	}
}

func (b *adminLogBuffer) Add(e adminLogEntry) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.nextID++
	e.ID = b.nextID

	b.buf[b.nextIdx] = e
	b.nextIdx++
	if b.nextIdx >= b.capacity {
		b.nextIdx = 0
		b.full = true
	}
}

func (b *adminLogBuffer) ListNewestFirst() []adminLogEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []adminLogEntry
	if !b.full && b.nextIdx == 0 {
		return nil
	}

	if b.full {
		out = make([]adminLogEntry, 0, b.capacity)
		out = append(out, b.buf[b.nextIdx:]...)
		out = append(out, b.buf[:b.nextIdx]...)
	} else {
		out = make([]adminLogEntry, 0, b.nextIdx)
		out = append(out, b.buf[:b.nextIdx]...)
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

func (app *App) adminEnabled() bool {
	return app != nil && strings.TrimSpace(app.Config.AdminPassword) != ""
}

func (app *App) requireAdminAuth(w http.ResponseWriter, r *http.Request) bool {
	if !app.adminEnabled() {
		http.NotFound(w, r)
		return false
	}

	user, pass, ok := parseBasicAuth(r.Header.Get("Authorization"))
	_ = user
	if !ok || pass != app.Config.AdminPassword {
		w.Header().Set("WWW-Authenticate", `Basic realm="banana-proxy-admin"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return false
	}
	return true
}

func parseBasicAuth(auth string) (user string, pass string, ok bool) {
	if auth == "" {
		return "", "", false
	}
	if !strings.HasPrefix(auth, "Basic ") {
		return "", "", false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(auth, "Basic "))
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(decoded) == 0 {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (app *App) handleAdmin(w http.ResponseWriter, r *http.Request) {
	if !app.adminEnabled() {
		http.NotFound(w, r)
		return
	}

	if !app.requireAdminAuth(w, r) {
		return
	}

	switch r.URL.Path {
	case "/admin", "/admin/":
		http.Redirect(w, r, "/admin/logs", http.StatusFound)
		return
	case "/admin/logs":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, adminLogsHTML)
		return
	case "/admin/api/logs":
		app.handleAdminAPILogs(w, r)
		return
	case "/admin/api/stats":
		app.handleAdminAPIStats(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func (app *App) handleAdminAPILogs(w http.ResponseWriter, r *http.Request) {
	var logs []adminLogEntry
	if app != nil && app.AdminLogs != nil {
		logs = app.AdminLogs.ListNewestFirst()
	}

	// Ensure image URLs are stable/deduped in output to keep UI simple.
	for i := range logs {
		logs[i].RequestRawImages = dedupeStrings(logs[i].RequestRawImages)
		logs[i].RequestRawImageCacheHits = dedupeStrings(logs[i].RequestRawImageCacheHits)
		logs[i].RequestUpstreamImgs = dedupeStrings(logs[i].RequestUpstreamImgs)
		logs[i].ResponseImages = dedupeStrings(logs[i].ResponseImages)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"items": logs,
	})
}

func (app *App) handleAdminAPIStats(w http.ResponseWriter, r *http.Request) {
	var total, errors, durationMs, hits int64
	if app != nil && app.AdminStats != nil {
		total = app.AdminStats.totalRequests.Load()
		errors = app.AdminStats.errorRequests.Load()
		durationMs = app.AdminStats.totalDurationMs.Load()
		hits = app.AdminStats.cacheHits.Load()
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"totalRequests":   total,
		"errorRequests":   errors,
		"totalDurationMs": durationMs,
		"cacheHits":       hits,
	})
}

func sanitizeJSONForAdminLog(raw []byte) (pretty string, imageURLs []string) {
	if len(raw) == 0 {
		return "", nil
	}

	var root interface{}
	if err := json.Unmarshal(raw, &root); err != nil {
		return truncateForAdminLog(string(raw), adminMaxBodyBytesPerEntry), nil
	}

	imageURLs = redactInlineDataAndCollectImageURLs(root)

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return truncateForAdminLog(string(raw), adminMaxBodyBytesPerEntry), imageURLs
	}
	return truncateForAdminLog(string(out), adminMaxBodyBytesPerEntry), imageURLs
}

func redactInlineDataAndCollectImageURLs(root interface{}) []string {
	var urls []string

	var walk func(v interface{})
	walk = func(v interface{}) {
		switch node := v.(type) {
		case map[string]interface{}:
			if inline, ok := node["inlineData"].(map[string]interface{}); ok {
				if dataStr, ok := inline["data"].(string); ok && strings.TrimSpace(dataStr) != "" {
					if isHttpUrlString(dataStr) || strings.HasPrefix(dataStr, "/proxy/image") {
						urls = append(urls, dataStr)
					} else {
						inline["data"] = fmt.Sprintf("[base64 omitted len=%d]", len(dataStr))
					}
				}
			}
			if inline, ok := node["inline_data"].(map[string]interface{}); ok {
				if dataStr, ok := inline["data"].(string); ok && strings.TrimSpace(dataStr) != "" {
					if isHttpUrlString(dataStr) || strings.HasPrefix(dataStr, "/proxy/image") {
						urls = append(urls, dataStr)
					} else {
						inline["data"] = fmt.Sprintf("[base64 omitted len=%d]", len(dataStr))
					}
				}
			}
			for _, child := range node {
				walk(child)
			}
		case []interface{}:
			for _, child := range node {
				walk(child)
			}
		}
	}

	walk(root)
	return dedupeStrings(urls)
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func sortedKeys(set map[string]struct{}) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncateForAdminLog(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return s
	}
	if len(s) <= maxBytes {
		return s
	}
	cut := maxBytes
	// Keep tail marker small and avoid breaking UTF-8 too aggressively (best effort).
	marker := "\n...[truncated]"
	if cut > len(marker) {
		cut -= len(marker)
	}
	out := s[:cut] + marker
	return out
}

const adminLogsHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>banana-proxy 管理后台</title>
  <style>
    :root { color-scheme: dark; }
    body { margin: 0; font-family: ui-sans-serif, system-ui, -apple-system, Segoe UI, Arial; background: #0b1020; color: #e6e9f2; }
    header { position: sticky; top: 0; background: rgba(11,16,32,0.9); backdrop-filter: blur(8px); border-bottom: 1px solid rgba(255,255,255,0.08); padding: 14px 16px; z-index: 2; }
    h1 { margin: 0; font-size: 16px; font-weight: 600; }
    .sub { margin-top: 6px; font-size: 12px; color: rgba(230,233,242,0.7); }
    main { padding: 16px; max-width: 1200px; margin: 0 auto; }
    .toolbar { display: flex; gap: 8px; align-items: center; margin-bottom: 12px; }
    button { background: #1a2455; color: #e6e9f2; border: 1px solid rgba(255,255,255,0.12); padding: 8px 10px; border-radius: 8px; cursor: pointer; }
    button:hover { background: #223070; }
    .card { border: 1px solid rgba(255,255,255,0.10); border-radius: 12px; background: rgba(255,255,255,0.03); margin-bottom: 12px; overflow: hidden; }
    .card-head { display: flex; justify-content: space-between; gap: 12px; padding: 12px 12px; cursor: pointer; }
    .meta { font-size: 12px; color: rgba(230,233,242,0.75); }
    .title { font-weight: 600; }
    .pill { display:inline-block; padding: 2px 8px; border-radius: 999px; font-size: 12px; border: 1px solid rgba(255,255,255,0.12); color: rgba(230,233,242,0.85); }
    .ok { background: rgba(34,197,94,0.12); border-color: rgba(34,197,94,0.22); }
    .bad { background: rgba(239,68,68,0.12); border-color: rgba(239,68,68,0.22); }
    .body { display: none; padding: 12px; border-top: 1px solid rgba(255,255,255,0.08); }
    .grid { display: grid; grid-template-columns: 1fr; gap: 12px; }
    @media (min-width: 1000px) { .grid { grid-template-columns: 1fr 1fr 1fr; } }
    pre { white-space: pre-wrap; word-break: break-word; background: rgba(0,0,0,0.25); border: 1px solid rgba(255,255,255,0.08); border-radius: 10px; padding: 10px; margin: 0; font-size: 12px; line-height: 1.4; }
    .imgs { display: flex; gap: 10px; flex-wrap: wrap; margin-top: 8px; }
    .imgs a { position: relative; display: inline-block; border: 1px solid rgba(255,255,255,0.10); border-radius: 10px; overflow: hidden; background: rgba(0,0,0,0.25); }
    .imgs img { display: block; width: 140px; height: 140px; object-fit: contain; background: #000; }
    .badge { position: absolute; top: 6px; left: 6px; padding: 2px 8px; border-radius: 999px; font-size: 11px; font-weight: 600; background: rgba(34,197,94,0.88); color: #081018; border: 1px solid rgba(34,197,94,0.18); }
    .k { font-size: 12px; color: rgba(230,233,242,0.7); margin-bottom: 6px; }
    .muted { color: rgba(230,233,242,0.55); }
  </style>
</head>
<body>
  <header>
    <h1>banana-proxy 管理后台</h1>
    <div class="sub">最近 100 次 Gemini 请求（raw / upstream / downstream），Base64 自动省略，URL 图片可直接预览。</div>
  </header>
  <main>
    <div class="toolbar">
      <button id="refresh">刷新</button>
      <span class="meta muted" id="status"></span>
    </div>
    <div id="list"></div>
  </main>
  <script>
    const elList = document.getElementById('list');
    const elStatus = document.getElementById('status');
    const btnRefresh = document.getElementById('refresh');

    function escapeHTML(s) {
      return String(s)
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
    }

    function renderImgs(urls, hitUrls) {
      if (!urls || urls.length === 0) return '';
      const hit = new Set(hitUrls || []);
      const items = urls.map(u => {
        const safe = escapeHTML(u);
        const badge = hit.has(u) ? '<span class=\"badge\">CACHE</span>' : '';
        return '<a href=\"' + safe + '\" target=\"_blank\" rel=\"noreferrer\">' + badge + '<img src=\"' + safe + '\" alt=\"img\" /></a>';
      }).join('');
      return '<div class=\"imgs\">' + items + '</div>';
    }

    function pill(statusCode) {
      const ok = (statusCode >= 200 && statusCode < 300);
      const cls = ok ? 'ok' : 'bad';
      return '<span class=\"pill ' + cls + '\">' + (statusCode || 0) + '</span>';
    }

    function card(item) {
      const when = item.createdAt ? new Date(item.createdAt).toLocaleString() : '';
      const path = item.query ? (item.path + '?' + item.query) : item.path;
      const head =
        '<div class=\"card-head\">' +
          '<div>' +
            '<div class=\"title\">' + escapeHTML(item.method || '') + ' ' + escapeHTML(path || '') + ' ' + pill(item.statusCode) + '</div>' +
            '<div class=\"meta\">' + escapeHTML(when) + ' · output=' + escapeHTML(item.outputMode || '') + ' · stream=' + (item.isStream ? 'yes' : 'no') + ' · from=' + escapeHTML(item.remoteAddr || '') + '</div>' +
          '</div>' +
          '<div class=\"meta\">#' + escapeHTML(item.id || '') + '</div>' +
        '</div>';

      const body =
        '<div class=\"body\">' +
          '<div class=\"grid\">' +
            '<div>' +
              '<div class=\"k\">请求体（raw）</div>' +
              renderImgs(item.requestRawImages, item.requestRawImageCacheHits) +
              '<pre>' + escapeHTML(item.requestRaw || '') + '</pre>' +
            '</div>' +
            '<div>' +
              '<div class=\"k\">上游请求体（改写后）</div>' +
              renderImgs(item.requestUpstreamImages) +
              '<pre>' + escapeHTML(item.requestUpstream || '') + '</pre>' +
            '</div>' +
            '<div>' +
              '<div class=\"k\">下游响应体（最终）</div>' +
              renderImgs(item.responseImages) +
              '<pre>' + escapeHTML(item.responseDownstream || '') + '</pre>' +
            '</div>' +
          '</div>' +
        '</div>';

      const wrapper = document.createElement('div');
      wrapper.className = 'card';
      wrapper.innerHTML = head + body;
      wrapper.querySelector('.card-head').addEventListener('click', () => {
        const b = wrapper.querySelector('.body');
        b.style.display = (b.style.display === 'block') ? 'none' : 'block';
      });
      return wrapper;
    }

    async function refresh() {
      elStatus.textContent = '加载中...';
      elList.innerHTML = '';
      try {
        const res = await fetch('/admin/api/logs', { cache: 'no-store' });
        if (!res.ok) throw new Error('HTTP ' + res.status);
        const data = await res.json();
        const items = (data && data.items) ? data.items : [];
        elStatus.textContent = '共 ' + items.length + ' 条';
        for (const it of items) elList.appendChild(card(it));
      } catch (e) {
        elStatus.textContent = '加载失败：' + e.message;
      }
    }

    btnRefresh.addEventListener('click', refresh);
    refresh();
  </script>
</body>
</html>`
