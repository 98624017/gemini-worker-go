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
	StatusCode   int    `json:"statusCode"`
	DurationMs   int64  `json:"durationMs"`
	FinishReason string `json:"finishReason"`

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
    *, *::before, *::after { box-sizing: border-box; }
    body { margin: 0; font-family: ui-sans-serif, system-ui, -apple-system, "Segoe UI", Arial, sans-serif; background: #080e1f; color: #e2e8f0; line-height: 1.5; }

    /* ── Header ── */
    header { position: sticky; top: 0; z-index: 10; background: rgba(8,14,31,0.85); backdrop-filter: blur(12px); border-bottom: 1px solid rgba(255,255,255,0.07); padding: 12px 20px; display: flex; align-items: center; gap: 12px; }
    header h1 { margin: 0; font-size: 15px; font-weight: 700; letter-spacing: 0.01em; flex: 1; }
    .auto-refresh-label { display: flex; align-items: center; gap: 6px; font-size: 12px; color: rgba(226,232,240,0.6); cursor: pointer; user-select: none; }
    .toggle { position: relative; width: 32px; height: 18px; }
    .toggle input { opacity: 0; width: 0; height: 0; }
    .slider { position: absolute; inset: 0; background: rgba(255,255,255,0.12); border-radius: 999px; transition: background 0.2s; }
    .slider::before { content: ""; position: absolute; width: 12px; height: 12px; left: 3px; top: 3px; background: #fff; border-radius: 50%; transition: transform 0.2s; }
    .toggle input:checked + .slider { background: #3b82f6; }
    .toggle input:checked + .slider::before { transform: translateX(14px); }
    .btn { background: rgba(255,255,255,0.07); color: #e2e8f0; border: 1px solid rgba(255,255,255,0.10); padding: 6px 14px; border-radius: 8px; cursor: pointer; font-size: 13px; transition: background 0.15s; }
    .btn:hover { background: rgba(255,255,255,0.13); }

    /* ── Layout ── */
    main { padding: 20px; max-width: 1400px; margin: 0 auto; }

    /* ── Stats Cards ── */
    .stats { display: grid; grid-template-columns: repeat(5, 1fr); gap: 12px; margin-bottom: 20px; }
    @media (max-width: 900px) { .stats { grid-template-columns: repeat(2, 1fr); } }
    @media (max-width: 480px) { .stats { grid-template-columns: 1fr; } }
    .stat-card { background: rgba(255,255,255,0.03); border: 1px solid rgba(255,255,255,0.07); border-radius: 14px; padding: 16px 18px; border-left: 3px solid var(--accent); position: relative; overflow: hidden; }
    .stat-card::before { content: ""; position: absolute; inset: 0; background: linear-gradient(135deg, rgba(255,255,255,0.02) 0%, transparent 60%); pointer-events: none; }
    .stat-label { font-size: 11px; font-weight: 600; letter-spacing: 0.06em; text-transform: uppercase; color: rgba(226,232,240,0.5); margin-bottom: 8px; }
    .stat-value { font-size: 28px; font-weight: 700; letter-spacing: -0.02em; color: #f1f5f9; line-height: 1; }
    .stat-sub { font-size: 11px; color: rgba(226,232,240,0.45); margin-top: 5px; }
    .c-blue  { --accent: #3b82f6; }
    .c-green { --accent: #22c55e; }
    .c-amber { --accent: #f59e0b; }
    .c-red   { --accent: #ef4444; }
    .c-purple{ --accent: #a855f7; }

    /* ── Toolbar ── */
    .toolbar { display: flex; align-items: center; gap: 8px; margin-bottom: 14px; flex-wrap: wrap; }
    .filter-tabs { display: flex; background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.08); border-radius: 8px; overflow: hidden; }
    .filter-tab { padding: 5px 14px; font-size: 12px; cursor: pointer; color: rgba(226,232,240,0.6); border: none; background: none; transition: background 0.15s, color 0.15s; }
    .filter-tab.active { background: rgba(255,255,255,0.10); color: #f1f5f9; font-weight: 600; }
    .search { flex: 1; min-width: 160px; max-width: 280px; background: rgba(255,255,255,0.05); border: 1px solid rgba(255,255,255,0.09); border-radius: 8px; padding: 5px 12px; color: #e2e8f0; font-size: 13px; outline: none; }
    .search:focus { border-color: rgba(59,130,246,0.5); }
    .search::placeholder { color: rgba(226,232,240,0.35); }
    .count-badge { margin-left: auto; font-size: 12px; color: rgba(226,232,240,0.45); }

    /* ── Log List ── */
    .log-item { border: 1px solid rgba(255,255,255,0.07); border-radius: 10px; background: rgba(255,255,255,0.02); margin-bottom: 6px; overflow: hidden; transition: border-color 0.15s; }
    .log-item:hover { border-color: rgba(255,255,255,0.12); }
    .log-row { display: flex; align-items: center; gap: 10px; padding: 9px 14px; cursor: pointer; }
    .log-id { font-size: 11px; color: rgba(226,232,240,0.3); min-width: 36px; font-variant-numeric: tabular-nums; }
    .log-model { flex: 1; font-size: 13px; font-weight: 500; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
    .log-meta { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
    .log-dur { font-size: 12px; color: rgba(226,232,240,0.5); min-width: 48px; text-align: right; font-variant-numeric: tabular-nums; }
    .log-time { font-size: 11px; color: rgba(226,232,240,0.35); min-width: 60px; text-align: right; }
    .tag { display: inline-flex; align-items: center; padding: 1px 7px; border-radius: 999px; font-size: 11px; font-weight: 600; border: 1px solid transparent; }
    .tag-ok  { background: rgba(34,197,94,0.10);  border-color: rgba(34,197,94,0.20);  color: #86efac; }
    .tag-bad { background: rgba(239,68,68,0.10);  border-color: rgba(239,68,68,0.20);  color: #fca5a5; }
    .tag-stream { background: rgba(59,130,246,0.08); border-color: rgba(59,130,246,0.15); color: #93c5fd; }
    .tag-url    { background: rgba(168,85,247,0.08); border-color: rgba(168,85,247,0.15); color: #d8b4fe; }

    /* ── Log Detail ── */
    .log-detail { display: none; padding: 14px; border-top: 1px solid rgba(255,255,255,0.07); }
    .detail-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 14px; }
    @media (max-width: 760px) { .detail-grid { grid-template-columns: 1fr; } }
    .detail-col-label { font-size: 11px; font-weight: 600; letter-spacing: 0.05em; text-transform: uppercase; color: rgba(226,232,240,0.4); margin-bottom: 8px; }
    pre { margin: 0; white-space: pre-wrap; word-break: break-all; background: rgba(0,0,0,0.30); border: 1px solid rgba(255,255,255,0.07); border-radius: 8px; padding: 10px; font-size: 11.5px; line-height: 1.5; color: #cbd5e1; max-height: 320px; overflow-y: auto; }
    .imgs { display: flex; flex-wrap: wrap; gap: 8px; margin-bottom: 8px; }
    .img-thumb { position: relative; border: 1px solid rgba(255,255,255,0.09); border-radius: 8px; overflow: hidden; background: rgba(0,0,0,0.3); }
    .img-thumb img { display: block; width: 120px; height: 120px; object-fit: contain; }
    .cache-badge { position: absolute; top: 4px; left: 4px; padding: 1px 6px; border-radius: 999px; font-size: 10px; font-weight: 700; background: rgba(34,197,94,0.85); color: #052e16; }

    /* ── FinishReason Bar ── */
    .fr-bar { display: flex; align-items: center; gap: 6px; margin-bottom: 14px; flex-wrap: wrap; min-height: 0; }
    .fr-label { font-size: 11px; font-weight: 600; letter-spacing: 0.05em; text-transform: uppercase; color: rgba(226,232,240,0.35); margin-right: 2px; }
    .fr-btn { padding: 3px 10px; font-size: 11px; font-weight: 600; border-radius: 999px; border: 1px solid rgba(255,255,255,0.09); background: rgba(255,255,255,0.04); color: rgba(226,232,240,0.55); cursor: pointer; transition: background 0.15s, color 0.15s; }
    .fr-btn.active { background: rgba(99,102,241,0.15); border-color: rgba(99,102,241,0.35); color: #a5b4fc; }
    .fr-btn:hover { background: rgba(255,255,255,0.09); color: #e2e8f0; }
    .tag-fr { background: rgba(99,102,241,0.08); border-color: rgba(99,102,241,0.18); color: #a5b4fc; }

    /* ── Empty / Status ── */
    .empty { text-align: center; padding: 60px 0; color: rgba(226,232,240,0.3); font-size: 14px; }
    .status-line { font-size: 12px; color: rgba(226,232,240,0.4); }
  </style>
</head>
<body>
<header>
  <h1>banana-proxy 管理后台</h1>
  <label class="auto-refresh-label">
    <span class="toggle"><input type="checkbox" id="autoRefreshChk"><span class="slider"></span></span>
    自动刷新
  </label>
  <button class="btn" id="btnRefresh">刷新</button>
</header>
<main>
  <!-- Stats -->
  <div class="stats" id="statsRow">
    <div class="stat-card c-blue">  <div class="stat-label">总请求数</div><div class="stat-value" id="s-total">—</div><div class="stat-sub">自启动以来</div></div>
    <div class="stat-card c-green"> <div class="stat-label">成功率</div><div class="stat-value" id="s-ok">—</div><div class="stat-sub" id="s-ok-sub">—</div></div>
    <div class="stat-card c-amber"> <div class="stat-label">平均耗时</div><div class="stat-value" id="s-dur">—</div><div class="stat-sub">毫秒</div></div>
    <div class="stat-card c-red">   <div class="stat-label">错误数</div><div class="stat-value" id="s-err">—</div><div class="stat-sub">4xx / 5xx</div></div>
    <div class="stat-card c-purple"><div class="stat-label">缓存命中</div><div class="stat-value" id="s-cache">—</div><div class="stat-sub" id="s-cache-sub">—</div></div>
  </div>

  <!-- Toolbar -->
  <div class="toolbar">
    <div class="filter-tabs">
      <button class="filter-tab active" data-filter="all">全部</button>
      <button class="filter-tab" data-filter="ok">成功 2xx</button>
      <button class="filter-tab" data-filter="bad">失败 4xx+</button>
    </div>
    <input class="search" id="searchBox" type="search" placeholder="搜索路径 / 模型名…" />
    <span class="count-badge" id="countBadge"></span>
    <span class="status-line" id="statusLine"></span>
  </div>

  <!-- FinishReason filter bar (populated dynamically) -->
  <div id="frBar" class="fr-bar"></div>

  <!-- Log list -->
  <div id="logList"></div>
</main>
<script>
(function () {
  'use strict';

  // ── State ──────────────────────────────────────────
  let allItems = [];
  let filterMode = 'all';         // 'all' | 'ok' | 'bad'
  let finishReasonFilter = 'all'; // 'all' | <REASON_STRING>
  let searchText = '';
  let autoTimer = null;

  // ── DOM refs ───────────────────────────────────────
  const elList    = document.getElementById('logList');
  const elStatus  = document.getElementById('statusLine');
  const elCount   = document.getElementById('countBadge');
  const elSearch  = document.getElementById('searchBox');
  const chkAuto   = document.getElementById('autoRefreshChk');
  const btnRef    = document.getElementById('btnRefresh');

  // ── Helpers ────────────────────────────────────────
  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g,'&amp;').replace(/</g,'&lt;')
      .replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#39;');
  }

  function fmtDur(ms) {
    if (ms == null || ms <= 0) return '—';
    if (ms < 1000) return ms + 'ms';
    if (ms < 60000) return (ms/1000).toFixed(1) + 's';
    const m = Math.floor(ms/60000), s = Math.round((ms%60000)/1000);
    return m + 'm' + (s ? s + 's' : '');
  }

  function fmtNum(n) {
    return Number(n).toLocaleString('zh-CN');
  }

  function relTime(iso) {
    const diff = Date.now() - new Date(iso).getTime();
    const s = Math.round(diff/1000);
    if (s < 5)  return '刚刚';
    if (s < 60) return s + '秒前';
    const m = Math.round(s/60);
    if (m < 60) return m + '分钟前';
    const h = Math.round(m/60);
    if (h < 24) return h + '小时前';
    return Math.round(h/24) + '天前';
  }

  function extractModel(path) {
    const m = path && path.match(/models\/([^/:]+)/);
    return m ? m[1] : (path || '');
  }

  // ── Stats ──────────────────────────────────────────
  async function loadStats() {
    try {
      const r = await fetch('/admin/api/stats', { cache: 'no-store' });
      if (!r.ok) return;
      const d = await r.json();
      const total = d.totalRequests || 0;
      const errors = d.errorRequests || 0;
      const ok = total - errors;
      const okPct = total ? ((ok/total)*100).toFixed(1)+'%' : '—';
      const avgMs = total ? Math.round(d.totalDurationMs/total) : 0;
      const hits  = d.cacheHits || 0;
      const hitPct = total ? ((hits/total)*100).toFixed(1)+'%' : '—';

      document.getElementById('s-total').textContent  = fmtNum(total);
      document.getElementById('s-ok').textContent     = okPct;
      document.getElementById('s-ok-sub').textContent = fmtNum(ok) + ' 次成功';
      document.getElementById('s-dur').textContent    = total ? fmtNum(avgMs) : '—';
      document.getElementById('s-err').textContent    = fmtNum(errors);
      document.getElementById('s-cache').textContent  = hitPct;
      document.getElementById('s-cache-sub').textContent = fmtNum(hits) + ' 次命中';
    } catch (e) { console.error('[admin] loadStats failed:', e); }
  }

  // ── Logs ───────────────────────────────────────────

  // safeUrl: only allow http/https absolute URLs and root-relative paths (/...).
  // Rejects javascript: and other dangerous protocols.
  function safeUrl(u) {
    if (!u) return '';
    if (u.startsWith('/')) return u;
    try {
      const p = new URL(u);
      return (p.protocol === 'http:' || p.protocol === 'https:') ? u : '';
    } catch (_) { return ''; }
  }

  function renderImgs(urls, hits) {
    if (!urls || !urls.length) return '';
    const hitSet = new Set(hits || []);
    return '<div class="imgs">' + urls.map(u => {
      const safe = safeUrl(u);
      if (!safe) return '';
      const badge = hitSet.has(u) ? '<span class="cache-badge">CACHE</span>' : '';
      return '<a class="img-thumb" href="'+esc(safe)+'" target="_blank" rel="noreferrer">'
           + badge + '<img src="'+esc(safe)+'" alt="" loading="lazy"></a>';
    }).join('') + '</div>';
  }

  function buildRow(item) {
    const model  = extractModel(item.path);
    const isOk   = item.statusCode >= 200 && item.statusCode < 400;
    const tagStatus = isOk
      ? '<span class="tag tag-ok">'+esc(item.statusCode)+'</span>'
      : '<span class="tag tag-bad">'+esc(item.statusCode)+'</span>';
    const tagStream = item.isStream ? '<span class="tag tag-stream">stream</span>' : '';
    const tagOut = item.outputMode ? '<span class="tag tag-url">'+esc(item.outputMode)+'</span>' : '';
    const fr = (item.finishReason || '').toUpperCase();
    const tagFr = fr ? '<span class="tag tag-fr">'+esc(fr)+'</span>' : '';

    const absTime = item.createdAt ? new Date(item.createdAt).toLocaleString('zh-CN') : '';
    const rel = item.createdAt ? relTime(item.createdAt) : '';

    const row = '<div class="log-row">'
      + '<span class="log-id">#'+esc(item.id)+'</span>'
      + '<span class="log-model" title="'+esc(item.path)+'">'+esc(model)+'</span>'
      + '<span class="log-meta">'
      + tagStatus + tagStream + tagOut + tagFr
      + '<span class="log-dur">'+fmtDur(item.durationMs)+'</span>'
      + '<span class="log-time" title="'+esc(absTime)+'">'+esc(rel)+'</span>'
      + '</span>'
      + '</div>';

    const detail = '<div class="log-detail">'
      + '<div class="detail-grid">'
      + '<div>'
        + '<div class="detail-col-label">原始请求体</div>'
        + renderImgs(item.requestRawImages, item.requestRawImageCacheHits)
        + '<pre>'+esc(item.requestRaw || '')+'</pre>'
      + '</div>'
      + '<div>'
        + '<div class="detail-col-label">下游响应体</div>'
        + renderImgs(item.responseImages)
        + '<pre>'+esc(item.responseDownstream || '')+'</pre>'
      + '</div>'
      + '</div>'
      + '</div>';

    const el = document.createElement('div');
    el.className = 'log-item';
    el.dataset.status = isOk ? 'ok' : 'bad';
    el.dataset.fr = fr;
    el.dataset.search = (model + ' ' + item.path + ' ' + item.statusCode + ' ' + fr).toLowerCase();
    el.innerHTML = row + detail;
    el.querySelector('.log-row').addEventListener('click', () => {
      const d = el.querySelector('.log-detail');
      d.style.display = d.style.display === 'block' ? 'none' : 'block';
    });
    return el;
  }

  function applyFilter() {
    const q = searchText.toLowerCase();
    let shown = 0;
    const items = elList.querySelectorAll('.log-item');
    items.forEach(el => {
      const matchFilter = filterMode === 'all'
        || (filterMode === 'ok'  && el.dataset.status === 'ok')
        || (filterMode === 'bad' && el.dataset.status === 'bad');
      const matchFr = finishReasonFilter === 'all' || el.dataset.fr === finishReasonFilter;
      const matchSearch = !q || el.dataset.search.includes(q);
      const visible = matchFilter && matchFr && matchSearch;
      el.style.display = visible ? '' : 'none';
      if (visible) shown++;
    });
    elCount.textContent = shown === allItems.length
      ? '共 ' + allItems.length + ' 条'
      : '显示 ' + shown + ' / ' + allItems.length + ' 条';
  }

  // ── FinishReason Bar ───────────────────────────────
  function rebuildFrBar() {
    const frBar = document.getElementById('frBar');
    const counts = {};
    allItems.forEach(it => {
      const fr = (it.finishReason || '').toUpperCase();
      if (fr) counts[fr] = (counts[fr] || 0) + 1;
    });
    const keys = Object.keys(counts).sort();
    if (!keys.length) { frBar.innerHTML = ''; return; }

    let html = '<span class="fr-label">finishReason</span>';
    const allAct = finishReasonFilter === 'all' ? ' active' : '';
    html += '<button class="fr-btn'+allAct+'" data-reason="all">全部</button>';
    keys.forEach(k => {
      const act = finishReasonFilter === k ? ' active' : '';
      html += '<button class="fr-btn'+act+'" data-reason="'+esc(k)+'">'+esc(k)+' <span style="opacity:.6">('+counts[k]+')</span></button>';
    });
    frBar.innerHTML = html;
    frBar.querySelectorAll('.fr-btn').forEach(btn => {
      btn.addEventListener('click', () => {
        finishReasonFilter = btn.dataset.reason;
        frBar.querySelectorAll('.fr-btn').forEach(b => b.classList.remove('active'));
        btn.classList.add('active');
        applyFilter();
      });
    });
  }

  async function loadLogs() {
    elStatus.textContent = '加载中…';
    try {
      const r = await fetch('/admin/api/logs', { cache: 'no-store' });
      if (!r.ok) throw new Error('HTTP ' + r.status);
      const d = await r.json();
      allItems = (d && d.items) || [];
      elList.innerHTML = '';
      if (!allItems.length) {
        elList.innerHTML = '<div class="empty">暂无请求记录</div>';
      } else {
        const frag = document.createDocumentFragment();
        allItems.forEach(it => frag.appendChild(buildRow(it)));
        elList.appendChild(frag);
      }
      rebuildFrBar();
      applyFilter();
      elStatus.textContent = '更新于 ' + new Date().toLocaleTimeString('zh-CN');
    } catch (e) {
      elStatus.textContent = '加载失败：' + e.message;
    }
  }

  async function refresh() {
    await Promise.all([loadStats(), loadLogs()]);
  }

  // ── Auto-refresh ───────────────────────────────────
  chkAuto.addEventListener('change', () => {
    if (chkAuto.checked) {
      autoTimer = setInterval(refresh, 5000);
    } else {
      clearInterval(autoTimer);
      autoTimer = null;
    }
  });

  // ── Filter tabs ────────────────────────────────────
  document.querySelectorAll('.filter-tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.filter-tab').forEach(b => b.classList.remove('active'));
      btn.classList.add('active');
      filterMode = btn.dataset.filter;
      applyFilter();
    });
  });

  // ── Search ─────────────────────────────────────────
  elSearch.addEventListener('input', () => {
    searchText = elSearch.value;
    applyFilter();
  });

  // ── Init ───────────────────────────────────────────
  btnRef.addEventListener('click', refresh);
  refresh();
})();
</script>
</body>
</html>`
