# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 常用命令

```bash
# 运行（本地开发）
UPSTREAM_API_KEY="your-key" go run .

# 编译
go build -o gemini-worker-go .

# 运行所有测试
go test ./...

# 运行单个测试
go test -run TestFetchImageUrlAsInlineData_DiskCache_HitSkipsNetwork .

# 运行测试并显示详细输出
go test -v ./...

# Docker 构建
docker build -t gemini-worker-go .

# Docker 运行（最小配置）
docker run --rm -p 8787:8787 \
  -e UPSTREAM_API_KEY="your-key" \
  -e PUBLIC_BASE_URL="https://your-domain.example" \
  gemini-worker-go
```

## 架构概览

整个服务是一个**单二进制** Go 程序（`package main`），无外部依赖（`go.mod` 仅声明 `go 1.22`，stdlib only）。

### 文件职责

| 文件 | 职责 |
|------|------|
| `main.go` | 核心逻辑：HTTP 路由、请求改写、响应处理、图片上传、代理图片 |
| `inline_data_url_cache.go` | 请求侧 inlineData URL 的磁盘缓存（SHA256 key，LRU 淘汰，滑动 TTL） |
| `inline_data_url_background_fetch.go` | 前台超时后转后台继续下载的异步机制 |
| `admin_log_ui.go` | 管理后台：内存环形 Buffer（最近 100 条）+ 内嵌 HTML/JS 页面 |
| `response_image_dedup.go` | 预留占位（响应图片去重逻辑已内联至 `main.go`） |

### 主要数据流

```
下游请求
  ↓
handleGenerateContent / handleStreamGenerateContent
  ├─ 解析 Auth token（支持 baseUrl|apiKey,baseUrl2|apiKey2 双上游路由）
  ├─ convertRequestInlineDataUrlsToBase64（URL → base64，含磁盘缓存+后台预取）
  ├─ vip.crond 特殊请求改写（aspectRatio 追加文本 / imageSize → model 后缀）
  └─ 转发到上游
       ↓
  handleNonStreamResponse / handleStreamResponse（SSE）
       ├─ 响应图片去重（多图 → 保留 payload 最大的那张）
       └─ output=url：base64 → 上传图床 → 可选代理包装 URL
```

### 核心结构体

- **`App`**：持有所有依赖（Config、HTTP 客户端、InlineDataURLCache、InlineDataURLBackgroundFetcher、AdminLogs）
- **`Config`**：从环境变量读取，含网络超时、TLS 开关、域名 allowlist 等所有旋钮
- **`inlineDataURLDiskCache`**：`SHA256(url)` 为 key，`{key}.data` + `{key}.meta.json` 为存储格式；使用 `inflightGroup`（singleflight 模式）防止并发穿透
- **`inlineDataBackgroundFetcher`**：前台等待超时后将下载任务移交后台；仅复用进行中的同 URL 下载任务；成功后写磁盘缓存，完成即从内存 map 移除

### 关键设计决策

1. **fail-open**：图床上传失败、代理 URL 构建失败均回退而非 5xx，保证主链路可用
2. **后台抓图桥接**：仅对进行中的同 URL 下载做去重；完成态结果不继续保留在内存，后续复用只依赖磁盘缓存
3. **双上游路由**：token 格式 `url1|key1,url2|key2`，仅当 `imageConfig.imageSize` 为 `4k/4K` 时切换到第二上游
4. **管理后台默认关闭**：`ADMIN_PASSWORD` 为空时 `/admin/*` 返回 404，Base64 图片在日志中自动省略

## 重要环境变量

| 变量 | 说明 |
|------|------|
| `UPSTREAM_API_KEY` | **必填**，上游 Gemini API Key |
| `UPSTREAM_BASE_URL` | 上游 Base URL，默认 `https://magic666.top` |
| `PUBLIC_BASE_URL` | 对外代理 URL 前缀，用于 `output=url` 响应改写 |
| `ALLOWED_PROXY_DOMAINS` | 允许被 `/proxy/image` 代理的域名（逗号分隔） |
| `ADMIN_PASSWORD` | 管理后台密码，空则关闭 |
| `INLINE_DATA_URL_CACHE_DIR` | 磁盘缓存目录，空则关闭（容器推荐 `/tmp/inline-data-url-cache`） |
| `PORT` | 监听端口，默认 `8787` |
