# gemini-worker-go（go-implementation）

该目录是 `banana-proxy` 中 Gemini 兼容入口的 Go 实现，用于把下游的 Gemini 请求转发到上游，并在响应侧做必要的图片/返回体处理。

## Async Gateway 子项目

仓库内新增了独立子项目 [`async-gateway/`](async-gateway/README.md)，用于承载 A3 Async Local Gateway。

职责边界：

- 根目录 `gemini-worker-go`
  继续处理同步 Gemini 兼容入口，以及 URL 型 `inlineData` → base64、`output=url` 等现有下游适配逻辑。
- `async-gateway`
  负责异步任务受理、状态持久化、后台 worker 转发 `NewAPI`、最近 3 天任务查询、恢复扫描与 TTL 清理。
- `NewAPI`
  继续负责鉴权、账务和额度。

如果你要部署 A3 异步网关，优先查看：

- `async-gateway/README.md`
- `async-gateway/deploy/banana-async-gateway.env.example`
- `async-gateway/deploy/nginx.async_gateway.conf.example`

如果你要跑一次真实异步联调烟测，优先用：

- `async-gateway/scripts/run_live_smoke.sh`
- `async-gateway/cmd/banana-async-smoke`

如果你要先做不依赖真实上游的离线回归，优先用：

- `async-gateway/scripts/run_live_smoke_test.sh`
- `.github/workflows/async-gateway-ci.yml`

烟测支持两种输入模式：

- 默认 prompt 模式：只设置 `SMOKE_PROMPT`
- 完整请求体模式：设置 `SMOKE_BODY_FILE=/path/to/request.json`，直接提交完整 JSON body

## 对外 HTTP 入口（简要）

- `POST /v1beta/models/:model:generateContent`
- `POST /v1beta/models/:model:streamGenerateContent`（SSE）
- `GET /proxy/image?url=<escaped-url>`（兼容）
- `GET /proxy/image?u=<base64url(url)>`（推荐：避免响应里出现明文域名）

## 环境变量（重点）

> 说明：所有布尔开关都支持以下值：
> - 开启：`1` / `true` / `yes` / `y` / `on` / `enable` / `enabled`
> - 关闭：`0` / `false` / `no` / `n` / `off` / `disable` / `disabled` / `none`

### 必填/核心

#### `UPSTREAM_API_KEY`（必填）

上游 Gemini API Key。缺失会返回 `401`。

#### `UPSTREAM_BASE_URL`（可选）

上游 Base URL（不带路径）。默认：`https://magic666.top`。

服务会将请求转发到：`${UPSTREAM_BASE_URL}${原始请求路径}`。

### 下游 Header 覆盖上游（按请求）

除环境变量外，下游也可以在单次请求中通过 Header 覆盖上游 `baseUrl` 与 `apiKey`（优先级：`x-goog-api-key` > `Authorization`）：

- `x-goog-api-key: <token>`
- `Authorization: Bearer <token>`

`<token>` 支持以下格式：

1) 仅覆盖 `apiKey`：
- `<apiKey>`

2) 同时覆盖 `baseUrl` 与 `apiKey`：
- `<baseUrl>|<apiKey>`

3) 双上游（按 `imageSize` 路由）：
- `<baseUrl1>|<apiKey1>,<baseUrl2>|<apiKey2>`
- 默认使用 `baseUrl1/apiKey1`
- **仅当**请求体 `generationConfig.imageConfig.imageSize` 为 `4k/4K` 时，改用 `baseUrl2/apiKey2`

注意：
- `baseUrl` 必须包含 `http://` 或 `https://` scheme
- 当 token 包含逗号（`,`）但无法解析为两组合法的 `<baseUrl>|<apiKey>` 时，服务会返回 `400`（避免误路由）

#### `PORT`（可选）

监听端口。默认：`8787`（当缺失或非纯数字时会回退到默认值）。

### 图片代理与 allowlist

#### `PUBLIC_BASE_URL`（可选）

用于构造对外可访问的代理 URL 前缀：`${PUBLIC_BASE_URL}/proxy/image?...`，主要影响 `output=url` 场景下的返回体改写。

- 当 `PUBLIC_BASE_URL` 为空/非法时：代理包装会 **fail-open** 回退为“直出 URL”（避免因为缺少代理前缀导致 5xx）。
- 也可以显式禁用：`PUBLIC_BASE_URL=off|0|false|disable|disabled|no|none`（视为关闭代理包装）。

#### `ALLOWED_PROXY_DOMAINS`（可选，逗号分隔）

控制哪些 host 允许被 `/proxy/image` 代理拉取。

格式规则：
- `example.com`：精确匹配 host
- `.example.com`：后缀匹配（同时匹配 `example.com` 和 `*.example.com`）

注意：
- **一旦显式设置 `ALLOWED_PROXY_DOMAINS`，会覆盖默认列表**（不会自动合并）。如果你仍需代理默认图床域名，请把默认项也写进去。

默认 allowlist（当未设置 `ALLOWED_PROXY_DOMAINS` 时）：
- `ai.kefan.cn`
- `uguu.se`
- `.uguu.se`
- `.aitohumanize.com`
- `.xuancat.cn`

示例（包含 xuancat + 默认项）：

```bash
ALLOWED_PROXY_DOMAINS="ai.kefan.cn,uguu.se,.uguu.se,.aitohumanize.com,.xuancat.cn"
```

### `output=url` 行为开关

#### 多图返回兼容处理

少数异常上游会在同一个 candidate 的 `content.parts` 中错误返回两张 `inlineData` 图片；
这两张通常是**同一张图的不同分辨率**。

为兼容只支持单图结果的下游客户端，服务会：

- 仅保留 **payload 更大的那张图片**
- 丢弃其余图片 `part`
- 保留同一 `parts` 中的非图片内容（如 `text`）

该规则同时适用于：

- 默认 base64 返回
- `output=url`（会在上传图床前先丢掉较小图片，避免返回两个 URL）

#### `IMAGE_HOST_MODE`（默认 `legacy`）

控制标准 `inlineData.data=base64` 图片在 `output=url` 时上传到哪里。

- `legacy`：继续使用现有图床上传链路（`uguu -> kefan`）
- `r2`：仅上传到 R2
- `r2_then_legacy`：优先上传到 R2，失败后回退现有图床链路

返回规则：

- 最终来自 **legacy** 的 URL：继续受 `PROXY_STANDARD_OUTPUT_URLS` 控制
- 最终来自 **R2** 的 URL：始终直出 `R2_PUBLIC_BASE_URL/<objectKey>`，**不会**再套 `/proxy/image`

失败策略：

- `r2`：R2 上传失败后，保持当前 fail-open 行为，响应保留原始 base64
- `r2_then_legacy`：R2 上传失败后自动回退 legacy 图床；若 legacy 也失败，仍保持 fail-open

#### R2 相关环境变量

当 `IMAGE_HOST_MODE=r2|r2_then_legacy` 时，以下变量必填：

- `R2_ENDPOINT`
  - 例如：`https://<accountid>.r2.cloudflarestorage.com`
- `R2_BUCKET`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_PUBLIC_BASE_URL`
  - 对外返回的自定义公网域名前缀，例如：`https://img.example.com`

可选变量：

- `R2_OBJECT_PREFIX`
  - 默认 `images`
  - 对象 key 形如：`images/YYYY/MM/DD/<timestamp>-<rand>.<ext>`

启动校验：

- `legacy` 模式下忽略 R2 配置
- `r2` / `r2_then_legacy` 模式下，如果缺少任一必填项，服务会在启动时直接失败

#### R2 定时清理 Worker

仓库内提供了独立的 Cloudflare Worker 定时清理器，目录：

- `cloudflare/r2-cleaner/`

用途：

- 定时遍历 R2 bucket 中指定前缀的对象
- 删除 `uploaded` 时间早于阈值的对象
- 与 Go 服务实例数无关，适合多实例部署场景

默认配置：

- Cron：`*/30 * * * *`
- `R2_CLEANUP_PREFIX=images`
- `R2_CLEANUP_MAX_AGE_SECONDS=10800`

建议：

- 生产环境让 `R2_CLEANUP_PREFIX` 与 Go 服务的 `R2_OBJECT_PREFIX` 保持一致
- 默认只清理指定前缀；若把前缀留空，则会扫描整个 bucket

部署前需要先修改：

- `cloudflare/r2-cleaner/wrangler.jsonc` 中的 `bucket_name`

本地调试：

```bash
npx wrangler dev --config cloudflare/r2-cleaner/wrangler.jsonc --test-scheduled
```

启动后可访问：

```text
http://127.0.0.1:8787/__scheduled
```

来手动触发一次定时任务。

部署：

```bash
npx wrangler deploy --config cloudflare/r2-cleaner/wrangler.jsonc
```

#### `PROXY_STANDARD_OUTPUT_URLS`（默认开启）

控制“标准 Gemini 返回体（inlineData.data=Base64）→ 上传图床得到 URL”之后，是否进一步把图床 URL 包装为 `${PUBLIC_BASE_URL}/proxy/image?...`：

- `1`（默认）：对大多数图床 URL 进行代理包装（减少域名暴露/可用于配合缓存）；但当图床 host 为 `ai.kefan.cn` 时保持直出（减少一跳）。
- `0`：始终直出图床 URL（速度优先）。

#### `PROXY_SPECIAL_UPSTREAM_URLS`（默认开启）

控制“特殊上游返回体”（上游在 `text` 中用 Markdown `![image](...)` 给出图片 URL）在 `output=url` 时是否进行“域名隐藏式代理包装”：

- `1`（默认）：返回 `${PUBLIC_BASE_URL}/proxy/image?u=<base64url(url)>`（响应体中不直接出现明文域名），并触发一次后台预热（不阻塞主请求）。
- `0`：直出原始 URL（允许明文包含域名，速度优先；且不触发预热）。

#### `UPLOAD_SPECIAL_UPSTREAM_IMAGES`（默认关闭）

用于解决“特殊上游返回的图片 URL 有效期很短（分钟级）”的问题。

- `0`（默认）：保持现有逻辑（不在关键路径下载/上传）。
- `1`：当命中特殊上游且 `output=url` 时，会先下载图片并上传到现有图床，再用图床 URL 替代返回体中的图片链接；随后是否再包装代理 URL 仍受 `PROXY_SPECIAL_UPSTREAM_URLS` 控制。

失败策略：
- 下载或上传失败会记录日志并 **fail-open** 回退到现有行为（不因为图床/网络抖动导致整体请求失败）。

### vip.crond 特殊上游请求改写（请求侧）

当上游 baseUrl 为 `https://api.vip.crond.dev` 时，需要在“转发到上游前”额外做两类请求改写（其余仍沿用现有机制）。

#### `SPECIAL_UPSTREAM_REQUEST_REWRITE_BASE_URLS`（默认包含 `https://api.vip.crond.dev`，逗号分隔）

用于配置哪些 `baseUrl` 启用该请求改写规则（为未来可能的同类渠道预留扩展性）。

- 未设置时：默认启用 `https://api.vip.crond.dev`（零配置可用）
- 显式设置时：使用你配置的列表（建议包含 `https://api.vip.crond.dev`）

#### `SPECIAL_UPSTREAM_IMAGE_SIZE_SUFFIX_MODELS`（默认 `gemini-3-pro-image-preview`，逗号分隔）

用于配置哪些 `model` 需要启用 “`imageSize` → `model-2k/-4k`” 的后缀改写规则。

#### 规则说明

仅当本次请求解析出的 `upstream baseUrl` 命中 `SPECIAL_UPSTREAM_REQUEST_REWRITE_BASE_URLS` 时才生效：

1) 若请求体存在 `generationConfig.imageConfig.aspectRatio`：
- 在 `contents` 中找到最后一个 `role="user"` 的 `text` part
- 将 `-aspectRatio:<ratio>` 追加到该 `text` 末尾（示例：`...真人-aspectRatio:16:9`）

2) 对 `POST /v1beta/models/:model:generateContent` 与 `POST /v1beta/models/:model:streamGenerateContent`：
- 若 `:model` 命中 `SPECIAL_UPSTREAM_IMAGE_SIZE_SUFFIX_MODELS`
- 且 `generationConfig.imageConfig.imageSize` 为 `2k/2K` 或 `4k/4K`
- 则转发到上游的路径 model 片段改写为 `:model-2k` 或 `:model-4k`

### 观测/排障

#### `SLOW_LOG_THRESHOLD_MS`（默认 `100000`）

慢请求分解计时日志阈值（毫秒）。

- `<=0`：关闭慢日志
- `>0`：当总耗时超过阈值时输出 `[Slow Request]` / `[Slow NonStream]` 分解指标

### 管理后台（可选）

用于排障：查看最近 100 次 Gemini 请求的
- 下游原始请求体（raw）
- 改写后发往上游的请求体（upstream）
- 返回给下游的响应体（downstream）

并支持将 `inlineData.data` 为 URL 的图片在页面中直接渲染为缩略图。

注意：
- Base64 图片会被自动省略（以占位符替代），避免日志体积过大。
- 默认关闭；为安全起见，只有显式配置密码后才会启用 `/admin/*`。
- 使用 HTTP Basic Auth（建议仅在内网或反代 HTTPS 下使用）。

#### `ADMIN_PASSWORD`（默认关闭）

管理后台密码。为空或 `off/0/false` 等视为关闭（此时 `/admin/*` 返回 404）。

启用后访问：
- `GET /admin`（重定向到 `/admin/logs`）
- `GET /admin/logs`（页面）
- `GET /admin/api/logs`（JSON）

### 网络/TLS（inlineData 图片抓取与图床上传）

> 说明：你看到的 `net/http: TLS handshake timeout` 属于“TLS 握手阶段超时”，并不等同于“证书校验失败”。  
> 证书校验问题通常会表现为 `x509: certificate signed by unknown authority` / `x509: certificate has expired` 等错误。

#### `IMAGE_FETCH_TIMEOUT_MS`（默认 `20000`）

服务在“把请求体中的 `inlineData.data=http(s)://...` 抓取为 bytes 并转 Base64”时的总超时（毫秒）。

### 请求侧 inlineData URL 跨请求缓存（磁盘）

用于优化“同一张输入图片被反复尝试/快速重试”的场景：当下游请求体反复携带同一个 `inlineData.data` 图片 URL 时，服务可在 **TTL** 内复用本地缓存结果，避免重复从源站拉取。

说明：
- 仅作用于 **请求侧** `inlineData.data=http(s)://...` → Base64 的抓取路径（不影响 `/proxy/image` 的流式透传行为）。
- 缓存仅保存“成功拉取且大小 ≤ 10MB”的图片 bytes + mimeType。
- 默认关闭；启用后缓存落在**容器文件系统**（宿主机磁盘的容器写层）。若你希望跨容器重建持久化，请挂载 volume 到该目录。

#### `INLINE_DATA_URL_CACHE_DIR`（默认关闭）

缓存目录。为空/`off`/`0`/`false` 等视为关闭。

建议容器内路径：`/tmp/inline-data-url-cache`（如需持久化请挂载 volume）。

#### `INLINE_DATA_URL_CACHE_TTL_MS`（默认 `3600000`）

缓存 TTL（毫秒）。`<=0` 会视为关闭（即使设置了目录也不会启用缓存）。

#### `INLINE_DATA_URL_CACHE_MAX_BYTES`（默认 `1073741824`）

缓存目录允许占用的最大字节数（默认 1GiB）。超过后会按”近似 LRU（按文件 mtime）”淘汰旧条目。

#### `INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES`（默认 `104857600`，即 100MiB）

请求侧 inlineData URL 的**内存 L1 缓存**容量上限（字节）。

该缓存位于磁盘缓存之前：内存命中时直接返回，完全避免磁盘 I/O（通常可节省 1–5ms）。
- `0` / `off` / `false`：关闭内存缓存（磁盘 L2 仍正常工作）
- 单条记录超过上限时自动忽略（不写入内存缓存）
- 进程重启后冷启动；磁盘缓存作为 L2 warmup 来源
- 若检测到容器内存上限，默认值会自动按容器内存缩放，规则约为“容器内存的 1/40”，并限制在 `16MiB ~ 100MiB` 之间

注意：内存缓存作为磁盘缓存的 **L1 前置层**，需要磁盘缓存（`INLINE_DATA_URL_CACHE_DIR`）同时配置才能生效。若磁盘缓存未配置，内存缓存设置将被忽略。

### Go 运行时内存保护

为降低大图请求、批量响应改写和上传中转时的内存尖峰，服务会在检测到容器内存限制时自动做两件事：

- 自动把 Go 运行时内存上限设置为容器内存的约 `65%`
- 当未显式设置 `GOGC` 时，自动把 GC 百分比收紧到 `80`

补充说明：

- 若你已经显式设置 `GOMEMLIMIT`，服务不会覆盖它
- 若容器未暴露 cgroup 内存限制，则保持 Go 默认运行时行为
- 在大响应处理、SSE 转发、inlineData 改写后，服务会按阈值尝试触发 `GC` / `FreeOSMemory`，减少高水位内存滞留

### 请求侧 inlineData URL 后台抓图桥接

用于优化“图片源站较慢、前台请求不想一直阻塞”的场景。

行为说明：

- 当前台请求等待到 `INLINE_DATA_URL_BACKGROUND_FETCH_WAIT_TIMEOUT_MS` 仍未下载完成时，会返回“图片仍在后台下载，请稍后重试”。
- 同一个 URL 在**下载进行中**时，后续重试会复用同一个 in-flight 下载任务，避免重复向源站发起多次拉取。
- **下载一旦完成，任务会立即从内存 map 中移除**，不再在内存中保留完成态图片 bytes。
- 因此，**完成后的后续复用只依赖磁盘缓存**：
  - 若已启用 `INLINE_DATA_URL_CACHE_DIR` 且落盘成功，后续请求可命中磁盘缓存
  - 若未启用磁盘缓存，或此时尚未成功落盘，则后续请求会重新拉取源站

这意味着该机制的定位是：

- **进行中的下载去重**
- **慢下载转后台继续执行**

而**不是**“完成后继续把图片结果缓存到内存”。

#### `INLINE_DATA_URL_BACKGROUND_FETCH_WAIT_TIMEOUT_MS`（默认跟随 `IMAGE_FETCH_TIMEOUT_MS`）

前台请求愿意等待后台桥接结果的时长（毫秒）。

- 超过该时间仍未完成：本次请求返回超时提示，但后台下载可继续执行
- 仅支持正整数毫秒值
- 未设置、非数字或 `<=0` 时，会回退到默认等待策略（优先使用 `IMAGE_FETCH_TIMEOUT_MS`，再回退到后台总超时）

#### `INLINE_DATA_URL_BACKGROUND_FETCH_TOTAL_TIMEOUT_MS`（默认 `90000`）

单个后台下载任务的总超时（毫秒）。

- 超过后会放弃该后台任务
- 该值越大，慢源站重试更容易“捡到”进行中的下载
- 但值越大，慢任务在系统中占资源的时间也越久

#### `INLINE_DATA_URL_BACKGROUND_FETCH_MAX_INFLIGHT`（默认 `128`）

允许同时存在的后台下载任务上限。

- 达到上限后，新请求不会进入后台桥接，而是回退到原始直连下载逻辑
- 如果你的目标是压低内存和并发压力，建议把它显著调小，例如 `8` 或 `16`

#### `IMAGE_TLS_HANDSHAKE_TIMEOUT_MS`（默认 `15000`）

抓取图片时的 TLS 握手超时（毫秒）。当遇到公网 OSS/CDN 偶发慢握手时，可适当增大该值以减少 502。

#### `IMAGE_FETCH_EXTERNAL_PROXY_DOMAINS`（可选，逗号分隔）

当服务需要抓取请求体中的图片 URL（`inlineData.data=http(s)://...`）时，若目标 hostname 命中该列表，则会把抓取 URL 改写为外部代理：

- `https://gemini.xinbaoai.com/proxy/image?url=<escaped-original-url>`

用于绕过部分公网 OSS/CDN 在本机网络环境下偶发的 `net/http: TLS handshake timeout`。

匹配规则与 `ALLOWED_PROXY_DOMAINS` 一致：
- `example.com`：精确匹配
- `.example.com`：后缀匹配（同时匹配 `example.com` 和 `*.example.com`）

示例（阿里云 OSS 桶域名）：

```bash
IMAGE_FETCH_EXTERNAL_PROXY_DOMAINS="miratoon.oss-cn-hangzhou.aliyuncs.com"
# 或：匹配同 region 下所有 bucket
IMAGE_FETCH_EXTERNAL_PROXY_DOMAINS=".oss-cn-hangzhou.aliyuncs.com"
```

补充：该配置同样会作用于本服务 `/proxy/image` 的后端拉取（前提是目标仍需通过 `ALLOWED_PROXY_DOMAINS` 校验），因此也能改善经 `/proxy/image` 访问 OSS 时的握手稳定性。

#### `IMAGE_FETCH_INSECURE_SKIP_VERIFY`（默认关闭）

是否对“图片下载”跳过 TLS 证书校验（`InsecureSkipVerify`）。  
强烈不建议开启；仅在明确知道自己处于可信网络/企业 MITM 代理环境且短期排障时使用。

#### `UPLOAD_TIMEOUT_MS`（默认 `10000`）

服务在“将返回体中的 Base64 图片上传到图床得到 URL”时的总超时（毫秒）。

#### `UPLOAD_TLS_HANDSHAKE_TIMEOUT_MS`（默认 `10000`）

图床上传时的 TLS 握手超时（毫秒）。

#### `UPLOAD_INSECURE_SKIP_VERIFY`（默认关闭）

是否对“图床上传”跳过 TLS 证书校验（`InsecureSkipVerify`）。同样不建议开启，除非你非常清楚风险与边界。

## 快速运行（示例）

### 本地（PowerShell）

```powershell
$env:UPSTREAM_API_KEY="your-key"
$env:UPSTREAM_BASE_URL="https://magic666.top"
$env:PORT="8787"
$env:PUBLIC_BASE_URL="https://your-public-domain.example"
$env:ALLOWED_PROXY_DOMAINS="ai.kefan.cn,uguu.se,.uguu.se,.aitohumanize.com,.xuancat.cn"

go run .
```

### Docker

```bash
docker build -t gemini-worker-go .
docker run --rm -p 8787:8787 \
  -e UPSTREAM_API_KEY="your-key" \
  -e PUBLIC_BASE_URL="https://your-public-domain.example" \
  -e ALLOWED_PROXY_DOMAINS="ai.kefan.cn,uguu.se,.uguu.se,.aitohumanize.com,.xuancat.cn" \
  -e INLINE_DATA_URL_CACHE_DIR="/tmp/inline-data-url-cache" \
  -e INLINE_DATA_URL_CACHE_TTL_MS="3600000" \
  -e INLINE_DATA_URL_CACHE_MAX_BYTES="1073741824" \
  -v /data/inline-data-url-cache:/tmp/inline-data-url-cache \
  gemini-worker-go
```

### GHCR 公开镜像

仓库已提供根目录主服务的 GHCR 发布工作流：

- Workflow：`.github/workflows/gemini-worker-go-ghcr.yml`
- 镜像名：`ghcr.io/98624017/gemini-worker-go`
- 常用 tag：
  - `main`
  - `sha-<commit>`
  - `vX.Y.Z` / `X.Y.Z` / `X.Y` / `X`
  - `latest`（仅在推送 `v*` tag 时生成）

触发方式：

- push 到 `main` 且命中根目录服务相关文件
- push `v*` tag
- 手动触发 `workflow_dispatch`

首次发布到 GHCR 后，如果包默认还是私有，需要在 GitHub Packages 页面将
`gemini-worker-go` 的 visibility 手动切为 `Public`。
