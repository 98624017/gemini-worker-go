# Admin UI 重设计设计文档

**日期：** 2026-03-17
**方向：** UI/UX 重做 + 轻量统计后端
**方案：** 仪表盘风格，原子计数器统计，客户端过滤/搜索

---

## 背景

现有管理界面是一个内嵌在 Go 字符串常量里的简单 HTML 页面，功能基本够用但视觉粗糙、信息密度低、缺少统计和过滤能力。目标是推倒重来，打造一个真正舒适的仪表盘风格管理后台，同时不显著增加服务器开销。

---

## 设计决策

### 不引入外部 CSS/JS 框架

页面仍内嵌在 `admin_log_ui.go` 的字符串常量里，不依赖 CDN，离线可用，部署无额外资源。所有样式和逻辑手写。

### 统计数据用原子计数器

后端新增 4 个 `int64` 原子计数器（`sync/atomic`），在请求处理完成时更新：
- `TotalRequests`
- `ErrorRequests`（状态码 ≥ 400）
- `TotalDurationMs`（累计耗时，毫秒）
- `CacheHits`（inlineData URL 内存/磁盘缓存命中次数）

计数器全在内存，服务重启归零，无磁盘写入，开销可忽略不计。

### 过滤/搜索全部在客户端

100 条日志的过滤不需要后端参与，客户端 JS 实时处理即可。

---

## 页面结构

```
┌─────────────────────────────────────────────────┐
│  sticky header：banana-proxy 管理后台             │
│  右侧：自动刷新开关（默认关）+ 刷新按钮              │
├─────────────────────────────────────────────────┤
│  统计卡片行（5张，横向排列，窄屏换行）               │
│  [总请求] [成功率] [平均耗时] [错误数] [缓存命中率]  │
├─────────────────────────────────────────────────┤
│  过滤栏                                          │
│  [全部 | 成功 2xx | 失败 4xx+]  [搜索路径/模型名]  │
│  右侧：当前筛选 X / 共 Y 条                        │
├─────────────────────────────────────────────────┤
│  日志列表（折叠态行列表，点击展开详情）               │
└─────────────────────────────────────────────────┘
```

---

## 统计卡片

| 卡片 | 数据来源 | 颜色 |
|------|---------|------|
| 总请求数 | `stats.totalRequests` | 蓝色 |
| 成功率 | `(total - errors) / total * 100` | 绿色 |
| 平均耗时 | `stats.totalDurationMs / total` | 琥珀色 |
| 错误数 | `stats.errorRequests` | 红色 |
| 缓存命中率 | `stats.cacheHits / total * 100` | 紫色 |

卡片标注"自启动以来"，覆盖全量请求（不限于最近 100 条）。

---

## 日志列表行（折叠态）

每行展示：
```
#42  gemini-3-pro-image-preview  [200]  1m34s  stream  output=url  3分钟前
```

- **模型名**：从路径 `/v1beta/models/<model>:generateContent` 提取
- **耗时**：新增字段，需在 `adminLogEntry` 里记录 `DurationMs int64`
- **相对时间**：`3分钟前`，hover 显示绝对时间 tooltip
- **状态码 pill**：2xx 绿色，4xx/5xx 红色

---

## 日志详情（展开态）

点击行展开，显示两列：

```
┌──────────────────────┬──────────────────────┐
│  原始请求体（raw）    │  下游响应体（最终）    │
│  [图片预览行]        │  [图片预览行]         │
│  <pre>JSON</pre>     │  <pre>JSON</pre>      │
└──────────────────────┴──────────────────────┘
```

去掉"改写后上游请求体"列，保留请求图片预览（含缓存命中 CACHE badge）和响应图片预览。

---

## 自动刷新

- 默认**关闭**，header 右侧提供 toggle 开关
- 开启后每 **5 秒**自动调用 `/admin/api/logs` 和 `/admin/api/stats`
- 新请求追加到列表顶部，已展开的条目不受影响

---

## 后端改动清单

### 1. `admin_log_ui.go`

新增 `adminStats` 结构体：
```go
type adminStats struct {
    totalRequests    atomic.Int64
    errorRequests    atomic.Int64
    totalDurationMs  atomic.Int64
    cacheHits        atomic.Int64
}
```

新增 `/admin/api/stats` 路由和 handler，返回：
```json
{
  "totalRequests": 142,
  "errorRequests": 3,
  "totalDurationMs": 84200,
  "cacheHits": 67
}
```

### 2. `adminLogEntry` 新增字段

```go
DurationMs       int64  `json:"durationMs"`
RequestConvertedImgs []string `json:"requestConvertedImgs"`  // 已有字段确认保留
```

### 3. `main.go`

在请求处理完成时（现有日志记录点附近）调用：
```go
app.AdminStats.totalRequests.Add(1)
if statusCode >= 400 {
    app.AdminStats.errorRequests.Add(1)
}
app.AdminStats.totalDurationMs.Add(durationMs)
```

`cacheHits` 在 `convertRequestInlineDataUrlsToBase64` 缓存命中时递增。

---

## 不做的事（YAGNI）

- 不做持久化统计（重启归零是可接受的）
- 不做 WebSocket/SSE 实时推送（轮询已够用）
- 不做日志导出/下载
- 不做多页分页（100 条上限不变）
- 不引入任何外部依赖

---

## 视觉风格

- 延续深色主题（`#0b1020` 背景）
- 卡片用彩色左边框 + 微妙渐变背景区分
- 字体层级更清晰：大数字 + 小标签
- 过渡动画：卡片数字 countup、列表展开 smooth
- 响应式：统计卡片 ≥768px 一行五列，窄屏 2列/换行
