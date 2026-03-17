# Admin UI 重设计实现计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 将管理后台从简陋的日志展示页面重做为仪表盘风格的专业管理界面，含统计卡片、过滤搜索、耗时显示和自动刷新开关。

**Architecture:** 后端新增 `adminStats` 原子计数器结构体和 `/admin/api/stats` 接口，`adminLogEntry` 新增 `DurationMs` 字段；前端完整重写 `adminLogsHTML` 字符串常量，所有过滤/搜索逻辑在客户端执行，零额外服务器开销。

**Tech Stack:** Go 1.22 stdlib（`sync/atomic`），纯 HTML/CSS/JS 内嵌字符串，无外部依赖。

---

## Task 1: 后端 — adminStats 结构体 + DurationMs + 统计接口

**Files:**
- Modify: `admin_log_ui.go`（新增 `adminStats` 类型、handler、路由）
- Modify: `main.go`（`App` 结构体加字段、`adminLogEntry` 加字段、埋点）

---

### Step 1: 读取现有代码，定位关键位置

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
grep -n "type App struct" main.go
grep -n "AdminLogs.Add" main.go
grep -n "adminEntry.StatusCode" main.go | head -10
grep -n "CreatedAt.*time.Now" main.go
```

关键位置（已知）：
- `App` struct：main.go 第 128 行附近
- `adminEntry` 创建 + defer Add：main.go 第 636–648 行附近
- `adminLogEntry` struct：admin_log_ui.go 第 17 行

---

### Step 2: 在 `admin_log_ui.go` 新增 `adminStats` 结构体

在文件顶部 `import` 块中加入 `"sync/atomic"`（若未使用则 Go 编译器会报错，先确认是否已有）。

在 `adminLogBuffer` 结构体定义之后，新增：

```go
// adminStats tracks aggregate request counters since process start.
// All fields are accessed atomically; no lock needed.
type adminStats struct {
	totalRequests   atomic.Int64
	errorRequests   atomic.Int64
	totalDurationMs atomic.Int64
	cacheHits       atomic.Int64
}
```

> 注意：`atomic.Int64` 是 Go 1.19+ 的高层封装，项目用 Go 1.22，直接使用即可，无需 `sync/atomic.AddInt64`。

---

### Step 3: 在 `admin_log_ui.go` 新增 `/admin/api/stats` handler

在 `handleAdminAPILogs` 函数之后，新增：

```go
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
```

---

### Step 4: 在 `handleAdmin` 路由 switch 中注册新路由

找到 `handleAdmin` 函数的 switch 语句，在 `case "/admin/api/logs":` 之后加：

```go
case "/admin/api/stats":
    app.handleAdminAPIStats(w, r)
    return
```

---

### Step 5: 在 `App` 结构体中新增 `AdminStats` 字段

找到 `main.go` 中的 `type App struct`，在 `AdminLogs *adminLogBuffer` 之后加：

```go
AdminStats *adminStats
```

---

### Step 6: 在 `main()` 初始化 `AdminStats`

找到 `main.go` 中初始化 `app.AdminLogs` 的位置（`app.AdminLogs = newAdminLogBuffer(...)`），在其后加：

```go
app.AdminStats = &adminStats{}
```

---

### Step 7: 在 `adminLogEntry` 中新增 `DurationMs` 字段

在 `admin_log_ui.go` 的 `adminLogEntry` 结构体中，在 `StatusCode int` 之后加：

```go
DurationMs int64 `json:"durationMs"`
```

---

### Step 8: 在请求 defer 中填充 DurationMs 并更新统计计数器

找到 `main.go` 第 636–648 行附近的 `adminEntry` 创建代码：

```go
adminEntry = &adminLogEntry{
    CreatedAt:  time.Now(),
    ...
}
defer func() {
    app.AdminLogs.Add(*adminEntry)
}()
```

修改为：

```go
reqStart := time.Now()
adminEntry = &adminLogEntry{
    CreatedAt:  reqStart,
    ...
}
defer func() {
    durationMs := time.Since(reqStart).Milliseconds()
    adminEntry.DurationMs = durationMs
    app.AdminLogs.Add(*adminEntry)

    if app.AdminStats != nil {
        app.AdminStats.totalRequests.Add(1)
        if adminEntry.StatusCode >= 400 {
            app.AdminStats.errorRequests.Add(1)
        }
        app.AdminStats.totalDurationMs.Add(durationMs)
    }
}()
```

> 注意：`reqStart` 变量替代 `time.Now()` 直接内联，确保 defer 计算的是完整请求耗时。

---

### Step 9: 在 cacheHit observer 中递增全局计数器

找到 `main.go` 中 observer 定义（第 735 行附近）：

```go
observer = func(rawURL string, fromCache bool) {
    if !fromCache {
        return
    }
    cacheHitMu.Lock()
    cacheHits[rawURL] = struct{}{}
    cacheHitMu.Unlock()
}
```

修改为：

```go
observer = func(rawURL string, fromCache bool) {
    if !fromCache {
        return
    }
    cacheHitMu.Lock()
    cacheHits[rawURL] = struct{}{}
    cacheHitMu.Unlock()
    if app.AdminStats != nil {
        app.AdminStats.cacheHits.Add(1)
    }
}
```

---

### Step 10: 编译验证

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
go build ./...
```

Expected: 编译成功，无错误。

---

### Step 11: 运行测试

```bash
go test ./... -count=1
```

Expected: 全部 PASS。

---

### Step 12: 验证新接口可访问

```bash
# 启动服务（后台）
ADMIN_PASSWORD=test go run . &
sleep 2

# 访问 stats 接口
curl -s -u admin:test http://localhost:8787/admin/api/stats | python3 -m json.tool

# 停止服务
kill %1
```

Expected: 返回 JSON，含 `totalRequests`、`errorRequests`、`totalDurationMs`、`cacheHits` 四个字段（初始均为 0）。

---

### Step 13: Commit

```bash
git add admin_log_ui.go main.go
git commit -m "feat: add adminStats counters, DurationMs field, and /admin/api/stats endpoint"
```

---

## Task 2: 前端 — 完整重写 adminLogsHTML

**Files:**
- Modify: `admin_log_ui.go`（替换 `adminLogsHTML` 常量）

这是本次改动最大的任务。将现有的 `adminLogsHTML` 字符串常量（471 行末尾的 const）完整替换为新版本。

---

### Step 1: 理解新页面结构

新页面包含以下区域（自上而下）：

```
1. sticky header（标题 + 自动刷新开关 + 刷新按钮）
2. 统计卡片行（5 张：总请求/成功率/平均耗时/错误数/缓存命中率）
3. 过滤工具栏（全部|成功|失败 切换 + 搜索框 + 计数）
4. 日志列表（折叠行，点击展开详情）
```

---

### Step 2: 替换 `adminLogsHTML` 常量

将 `admin_log_ui.go` 末尾的 `const adminLogsHTML = \`...\`` 整体替换为以下内容：

````go
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

  <!-- Log list -->
  <div id="logList"></div>
</main>
<script>
(function () {
  'use strict';

  // ── State ──────────────────────────────────────────
  let allItems = [];
  let filterMode = 'all';   // 'all' | 'ok' | 'bad'
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
    } catch (_) {}
  }

  // ── Logs ───────────────────────────────────────────
  function renderImgs(urls, hits) {
    if (!urls || !urls.length) return '';
    const hitSet = new Set(hits || []);
    return '<div class="imgs">' + urls.map(u => {
      const badge = hitSet.has(u) ? '<span class="cache-badge">CACHE</span>' : '';
      return '<a class="img-thumb" href="'+esc(u)+'" target="_blank" rel="noreferrer">'
           + badge + '<img src="'+esc(u)+'" alt="" loading="lazy"></a>';
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

    const absTime = item.createdAt ? new Date(item.createdAt).toLocaleString('zh-CN') : '';
    const rel = item.createdAt ? relTime(item.createdAt) : '';

    const row = '<div class="log-row">'
      + '<span class="log-id">#'+esc(item.id)+'</span>'
      + '<span class="log-model" title="'+esc(item.path)+'">'+esc(model)+'</span>'
      + '<span class="log-meta">'
      + tagStatus + tagStream + tagOut
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
    el.dataset.search = (model + ' ' + item.path + ' ' + item.statusCode).toLowerCase();
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
      const matchSearch = !q || el.dataset.search.includes(q);
      const visible = matchFilter && matchSearch;
      el.style.display = visible ? '' : 'none';
      if (visible) shown++;
    });
    elCount.textContent = shown === allItems.length
      ? '共 ' + allItems.length + ' 条'
      : '显示 ' + shown + ' / ' + allItems.length + ' 条';
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
````

---

### Step 3: 编译验证

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
go build ./...
```

Expected: 编译成功，无错误。

---

### Step 4: 运行测试

```bash
go test ./... -count=1
```

Expected: 全部 PASS。

---

### Step 5: 浏览器验证

```bash
# 启动服务
ADMIN_PASSWORD=test go run . &
sleep 2
echo "访问 http://localhost:8787/admin/logs"
```

打开浏览器访问 `http://localhost:8787/admin/logs`（Basic Auth: 用户名任意，密码 `test`）。

验证清单：
- [ ] 页面正常渲染，无 JS 报错
- [ ] 5 张统计卡片显示（初始为 0 或 —）
- [ ] 过滤 tab（全部/成功/失败）可切换
- [ ] 搜索框输入有实时过滤效果
- [ ] 日志条目点击可展开/折叠
- [ ] 展开后显示两列（原始请求体 / 下游响应体）
- [ ] 自动刷新开关可 toggle

```bash
kill %1
```

---

### Step 6: Commit

```bash
git add admin_log_ui.go
git commit -m "feat: redesign admin UI with dashboard stats, filter/search, and auto-refresh toggle"
```

---

## Final Verification

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
go test ./... -count=1 -race
go build -o gemini-worker-go .
echo "Build OK, size: $(du -sh gemini-worker-go)"
```

Expected:
- 全部测试 PASS，race detector 无报告
- 二进制正常构建
