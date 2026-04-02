# 香蕉Pro 图像生成 API 异步化改造
**伪装 Sora 上游 · 需求与架构文档 v4.0**

涉及系统：客户端 · NewAPI · CF Worker · CF Workflows · CF R2 · Gemini 上游

---

## 一、背景与目标

### 1.1 现有链路痛点

当前图像生成链路为全程同步阻塞，耗时 30 ～ 420 秒，存在两类问题：

- **丢图**：移动端息屏/网络切换、网关超时（Nginx 默认 60s）、CF 层超 100s、用户劣质 VPN 网络，均会导致连接中断。NewAPI 与客户端同步长连接断开时上游往往仍在生图，无法撤销，后续 NewAPI 得到上游返图却无法发送回用户，用户也无途径找回该成功生成的图片，丢图且扣费不可恢复。
- **资源占用**：大量挂起的长连接持续占用服务端连接池与内存，生图耗时越长积压越严重，压缩整体并发能力。

### 1.2 改造思路

将 CF Worker 伪装为 NewAPI 的 Sora 视频渠道上游。客户端按 Sora 协议提交请求，NewAPI 全权负责异步轮询调度、成功扣费与失败退费，CF Worker 只负责协议转换与图像生成。

**关键收益：**
- 计费/退费完全委托 NewAPI 已验证的代码路径，零新增分布式事务风险
- CF Worker 不持有任何密钥或计费状态，职责单纯：收请求 → 转协议 → 异步生图 → 返状态
- 文字模型等现有链路零改动

---

## 二、整体架构

### 2.1 链路对比

**原始同步链路：**
```
客户端 ──同步HTTP──▶ NewAPI（计费/鉴权）──透传──▶ 真实上游
               ◀──────────── 长链接维持等待 30~420s ────────────▶
```

**改造后链路：**
```
── 图像生成请求
      │
      ▼
   NewAPI（Sora渠道）                      ← 客户端走标准 Sora 协议
      │  POST /v1/videos (application/json)
      ▼
   CF Worker（伪装成 Sora 上游）
      │  立即返回 { id, status:"processing" }
      │  后台触发 CF Workflow
      ▼
   CF Workflow（异步执行）
      ├─ Step1: 调真实图像 API（含多渠道切换 + 参考图拉取）
      ├─ Step2: 图片写入 uguu / R2
      └─ Step3: 结果写入 KV，标记完成

   NewAPI 主动轮询 GET /v1/videos/{id}     ← NewAPI 原生轮询机制
      ▼
   CF Worker 查 KV 返回状态/图片URL
      ▼
   NewAPI 判断 succeeded → 扣费 + 返回客户端
   NewAPI 判断 failed    → 退费 + 返回客户端
```

### 2.2 职责边界

| 层级 | 组件 | 职责 | 是否需要改动 |
|---|---|---|---|
| 客户端 | 客户端应用 | 走 Sora 协议请求 NewAPI，处理结果 | 最小改动：切换接口路径 |
| 计费/鉴权 | NewAPI | 鉴权、轮询调度、扣费、退费 | 零改动 |
| 协议适配 | CF Worker | 接收 Sora 请求，转换为 Gemini 请求，返回 Sora 响应 | 新开发（核心） |
| 异步执行 | CF Workflows | 后台生图，Step 级持久化与重试 | 新开发 |
| 图片存储 | CF R2 | 持久化存储图片，提供 CDN 链接 | 新配置 |
| 状态缓存 | CF KV | 缓存任务状态，响应 NewAPI 轮询 | 新配置 |
| 图像生成 | 真实上游 | 实际执行图像生成 | 零改动 |

---

## 三、NewAPI Sora 渠道协议规范

真实 Sora 的 `url` 字段指向视频文件公网 URL，本方案完全复用相同响应结构，`url` 改为 uguu.se 或 CF R2 的图片公网链接。NewAPI 对 `url` 字段只做透传，不校验内容类型。

### 3.1 提交图像生成任务

```
POST /v1/videos
Content-Type: application/json
→ 201 Created
```

**请求体字段：**

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| model | string | 是 | 目标模型名，直接用于构造上游端点路径 |
| prompt | string | 是 | 图像描述文本 |
| aspect_ratio | string | 否 | 宽高比，如 16:9 |
| image_size | string | 否 | 分辨率，如 4K |
| image | array | 否 | 参考图片，最多 8 张，每项为公网 URL 或 base64 |

**CF Worker 立即返回（不等待生图）：**
```json
{
  "id": "img_<uuid>",
  "object": "video",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1710000000,
  "status": "processing",
  "progress": 0
}
```

> 响应须在 300ms 内返回。返回后立即触发 CF Workflow 异步执行。`id` 使用 `img_` 前缀 + UUIDv4。

### 3.2 轮询任务状态

```
GET /v1/videos/{video_id}     ← NewAPI 主动轮询
```

**执行中：**
```json
{
  "id": "img_<uuid>", "object": "video", "model": "gemini-3-pro-image-preview",
  "status": "processing",
  "progress": 50
}
```

**成功：**
```json
{
  "id": "img_<uuid>",
  "object": "video",
  "model": "gemini-3-pro-image-preview",
  "status": "succeeded",
  "progress": 100,
  "expires_at": 1712592000,
  "aspect_ratio": "16:9",
  "image_size": "4K",
  "seconds": 1,
  "url": "https://uguu.se/xxx.png",
  "description": "这是根据您的提示生成的图片..."
}
```

> `seconds` 为数值类型整数 `1`，NewAPI 用于计费公式，禁止以字符串形式返回。

**失败：**
```json
{
  "id": "img_<uuid>",
  "object": "video",
  "model": "gemini-3-pro-image-preview",
  "status": "failed",
  "error": { "code": "upstream_unavailable", "message": "all channels exhausted" }
}
```

NewAPI 对状态的处理：`processing` → 继续轮询；`succeeded` → 扣费并返回客户端；`failed` → 退还积分。

### 3.3 获取图片内容（可选）

```
GET /v1/videos/{video_id}/content
→ HTTP 302  Location: https://...图片链接
```

---

## 四、计费设计

### 4.1 计费公式

```
费用 = 模型倍率 × seconds × 每秒单价
```

CF Worker 所有响应中 `seconds` 固定返回数值 `1`，在 NewAPI 渠道配置中设定每秒单价为目标费用，实现按次固定扣费。

**换算示例**（定价 0.02 元/张，积分单位 0.000001 元）：
```
模型倍率 = 1，seconds = 1，每秒单价 = 20000
→ 每次扣 20,000 积分 = 0.02 元
```

### 4.2 计费全链路

| 阶段 | 执行方 | 时机 |
|---|---|---|
| 预扣 | NewAPI | 用户提交请求时，冻结积分 |
| 确认扣 | NewAPI | 收到 `succeeded` 后自动扣费 |
| 退还 | NewAPI | 收到 `failed` 后自动退还 |

CF Worker 不持有任何计费状态，不调用任何计费接口。

---

## 五、CF Worker 详细功能需求

### 5.1 端点实现清单

| 端点 | 方法 | 说明 |
|---|---|---|
| /v1/videos | POST | 接收提交，触发 Workflow，立即返回 processing |
| /v1/videos/{id} | GET | 查 KV 返回当前状态，轮询核心端点 |
| /v1/videos/{id}/content | GET | 302 重定向到图片地址 |

### 5.2 提交端点处理流程

```
1. 验证网关鉴权：Authorization header 与 GATEWAY_AUTH_TOKEN 比对，不匹配返回 401
2. 解析 JSON 请求体，提取 prompt / model / aspect_ratio / image_size / image
3. 入参校验（见 §8.1），不合规返回 400
4. 生成 task_id = "img_" + crypto.randomUUID()
5. 写入 KV：status="processing"，TTL=15min
6. 触发 Workflow：传入 task_id、prompt、model、aspect_ratio、image_size、image
   （上游 Key 由 Workflow 内部从 env.UPSTREAM_CHANNEL_KEYS 读取，不通过参数传递）
7. 返回 201：{ id, status:"processing", progress:0, ... }

⚠ 步骤 6 触发失败时，将 KV 状态置为 failed
```

> **每次提交均创建新任务，不对 prompt 内容做幂等缓存。** 图像扩散模型天然具有随机性，用户以相同 prompt 多次提交是正常的"抽卡"行为，系统必须每次都发起真实调用，返回新生成的图片。

### 5.3 轮询端点处理流程

```
1. 从路径提取 task_id
2. 查询 KV
3a. KV 命中 → 直接返回 KV 中存储的响应体
3b. KV 未命中 → 强制查询 Workflow 实例状态（兜底，见下文）
4. task_id 不存在（KV miss 且 Workflow 无记录）→ 返回 404
```

**KV 未命中兜底逻辑（强制，不可跳过）：**

CF KV 为最终一致性存储，Workflow Step 3 写入 `succeeded/failed` 后，全球边缘节点同步延迟可达数十秒。若此时 NewAPI 轮询打到尚未同步的节点，将读到 miss，若直接返回 404 会导致 NewAPI 误判任务丢失并提前退费。

```
KV miss 后：
  1. 调用 CF Workflows API 查询该 task_id 对应的实例状态（权威来源）
  2a. 实例状态为 running / queued → 返回 { status: "processing", progress: 50 }
  2b. 实例状态为 complete       → 说明 KV 写入尚未同步，返回 { status: "processing" }
                                   并触发异步 KV 补写（重新写入 succeeded 状态）
  2c. 实例状态为 errored        → 返回 { status: "failed", error: { code: "unknown_error" } }
  2d. 实例不存在（真正 404）    → 返回 404

⚠ 任何情况下，禁止在未查询 Workflow 实例状态前返回 404。
```

### 5.4 上游响应类型

| 上游响应类型 | 典型渠道 | 处理方式 |
|---|---|---|
| 返回 base64 图片数据 | 大多数标准图像生成 API | 解码后按图床策略上传，获取公网 URL |
| 返回图片公网 URL | 部分特殊渠道（如代理转发型） | 可配置：直接透传 URL，或先下载再按图床策略上传 |

### 5.5 图片托管策略（IMAGE_HOST_MODE）

**模式一：uguu 优先，R2 兜底（IMAGE_HOST_MODE=uguu）**

```
1. 获取 base64 图片数据
2. POST https://uguu.se/api.php?d=upload-tool（multipart，字段名 files[]），超时 30s
3a. 成功 → 取响应 URL 写入 KV
3b. 超时（30s）或失败 → 流式写入 CF R2，取 R2 链接
```

注意：uguu.se 文件保留时长 3 小时（服务端固定，不可调）。客户端展示时应提示用户及时保存。

**模式二：CF R2（IMAGE_HOST_MODE=r2）**

```
1. 获取 base64 图片数据
2. 流式写入 CF R2（ReadableStream pipe，禁止整图 buffer 进内存）
3. 取 R2 公开链接写入 KV
```

| 对比项 | uguu 优先 | R2 |
|---|---|---|
| 存储成本 | 主路径零成本 | 10GB 免费，超出 $0.015/GB/月 |
| 有效期 | 3 小时（不可调） | 7 天 Lifecycle Rule |
| 可靠性 | 依赖第三方，有 R2 兜底 | 完全自控 |
| 推荐 | ✅ 日常首选 | 需持久化时启用 |

### 5.6 Workflow 执行步骤与超时预算

**总超时：12 分钟**

最坏情况：3 渠道 × 每渠道重试 2 次 × 30s 慢响应 + 退避 ≈ 5 分钟，12 分钟为 2.4 倍余量。KV processing TTL 须 ≥ 12 分钟，设为 15 分钟。

| Step | 名称 | 超时 | 核心行为 | 失败处理 |
|---|---|---|---|---|
| Step 1 | 调用图像 API（含多渠道切换 + 参考图拉取） | 10 min | 遍历 UPSTREAM_CHANNEL_KEYS，按错误类型决定换渠道或退避重试（见 §5.7）；参考图 URL 在此步骤内拉取（超时 10s/张，大小 ≤ 10MB） | 全部渠道耗尽 → errored；参考图拉取失败 → 写入 failed，code: image_fetch_failed |
| Step 2 | 图片托管 | 2 min | uguu 优先（30s 超时），失败降级 R2；流式处理，禁止整图 buffer | uguu 失败降级 R2；R2 失败重试 2 次 |
| Step 3 | 更新 KV 终态 | 30s | 写入 succeeded/failed 状态、URL、description | 失败重试 3 次 |

Step 1 或 Step 2 全部重试耗尽后，Workflow errored。错误处理钩子中将 KV 状态置为 failed，NewAPI 下次轮询收到 failed 后自动触发退费。

### 5.7 Step 1 多渠道切换与重试逻辑

NewAPI 的渠道切换机制在异步模式下失效（NewAPI 只见到 202，从未见到真实上游错误），所有容错下沉到 Workflow Step 1 内部。

`UPSTREAM_CHANNEL_KEYS` 中每一项是一个独立第三方上游渠道的 API Key，每个渠道通常只配置一个 key。换"key"的本质是换渠道，而不是同一个上游服务的多凭证轮换。

**决策总表：**

| HTTP 状态 | 错误类型 / reason | 处理策略 | 说明 |
|---|---|---|---|
| 200 | — | ✅ 成功，进入 Step 2 | |
| 429 | RATE_LIMIT_EXCEEDED | 直接换下一个渠道 | 限流，切换更稳定的渠道 |
| 429 | RESOURCE_EXHAUSTED | 直接换下一个渠道 | 当日配额耗尽，重试无意义 |
| 429 | reason 无法解析 | 直接换下一个渠道 | 保守处理，尽快换渠道 |
| 500 | Internal Server Error | 换下一个渠道 | 上游服务内部异常 |
| 502 | Bad Gateway | 换下一个渠道 | 上游网关异常，通常是暂时性的 |
| 503 | Service Unavailable | 短暂退避（5s）后换下一个渠道 | 服务临时不可用 |
| 504 | Gateway Timeout | 换下一个渠道 | 上游响应超时 |
| 408 | Request Timeout | 重试同一渠道 1 次，仍失败则换渠道 | 请求超时，可能是网络抖动 |
| fetch 超时 | AbortError / 网络层超时 | 重试同一渠道 1 次，仍失败则换渠道 | Worker 侧 fetch 超时，可能是网络抖动 |
| 401 | Unauthorized | 换下一个渠道 | API Key 无效或已吊销 |
| 403 | Permission Denied / 模型无权限 | 换下一个渠道 | 该渠道无此模型权限 |
| 403 | 内容安全拦截（请求级） | 立即 throw，不换渠道 | prompt/图片触发安全策略，换渠道无意义 |
| 400 | Bad Request / 参数错误 | 立即 throw，不换渠道 | 请求体格式问题，换渠道无法解决 |
| 404 | Model Not Found | 立即 throw，不换渠道 | 模型名错误，换渠道无法解决 |
| 413 | Payload Too Large | 立即 throw，不换渠道 | 请求体超限，换渠道无法解决 |
| 全部渠道耗尽 | — | throw → errored，code = upstream_unavailable | |

**伪代码实现：**

```
配置：UPSTREAM_CHANNEL_KEYS = ["渠道A_key", "渠道B_key", "渠道C_key"]
      （CF Worker Secret，JSON 数组，每项对应一个独立的第三方上游渠道）

for each channelKey in UPSTREAM_CHANNEL_KEYS:
  retryCount = 0

  while retryCount <= 1:   // 每渠道最多 2 次尝试（含首次）
    try:
      result = await fetchWithTimeout(callUpstream(channelKey, prompt, ...), timeout=60s)
    catch (AbortError | NetworkError):
      // fetch 层超时或网络错误
      if retryCount < 1: retryCount++; continue same channel
      else: break → 换下一个渠道

    if result.ok: return result  // ✅ 成功

    switch result.status:

      case 429:
        break → 换下一个渠道   // 所有 429 类型（含限流/配额耗尽）直接换渠道

      case 500, 502, 504:
        break → 换下一个渠道               // 上游不稳定，直接换

      case 503:
        sleep(5s); break → 换下一个渠道   // 服务不可用，短暂退避后换

      case 408:
        if retryCount < 1: retryCount++; continue same channel
        else: break → 换下一个渠道

      case 401, 403:
        if isContentSafetyBlock(result):
          throw ContentPolicyError          // 内容拦截，不换渠道
        else:
          break → 换下一个渠道             // key 无权限，换渠道

      case 400, 404, 413:
        throw InvalidRequestError          // 参数问题，立即失败，不换渠道

      default:
        break → 换下一个渠道              // 未知错误，保守换渠道

// 所有渠道全部耗尽
throw AllChannelsExhaustedError → errored，error.code = "upstream_unavailable"
```

> **慢速 429 说明：** 部分上游需等待 30 秒以上才返回 429。同步链路下 NewAPI 会超时切渠道，但异步模式下 NewAPI 不参与，Step 1 的 10 分钟超时足以覆盖多轮慢速 429 + 换渠道的完整流程。

---

## 六、NewAPI 渠道配置

### 6.1 新建 Sora 渠道

| 配置项 | 值 | 说明 |
|---|---|---|
| 渠道类型 | OpenAI 视频（Sora 格式） | |
| 渠道名称 | banana-image-proxy | 自定义 |
| API Base URL | https://your-worker.workers.dev | CF Worker 部署地址 |
| API Key | 自定义鉴权 Token（如 banana-gw-token-xxx） | NewAPI 调 CF Worker 时携带，CF Worker 验证合法性；与真实上游 Key 无关 |
| 支持的模型 | gemini-3-pro-image-preview | |
| 每秒单价 | 按定价换算的固定值 | 配合 seconds=1 实现按次计费 |

### 6.2 CF Worker 上游 Key 配置

```bash
wrangler secret put GATEWAY_AUTH_TOKEN      # NewAPI → CF Worker 的鉴权 token
wrangler secret put UPSTREAM_CHANNEL_KEYS   # 各上游渠道的 API Key，每项对应一个独立渠道
                                            # JSON 数组，如 ["渠道A_key","渠道B_key","渠道C_key"]
```

```js
// CF Worker 鉴权
const incoming = request.headers.get("Authorization")?.replace("Bearer ", "")
if (incoming !== env.GATEWAY_AUTH_TOKEN) return new Response("Unauthorized", { status: 401 })

// Step 1 遍历渠道
const channelKeys = JSON.parse(env.UPSTREAM_CHANNEL_KEYS)
for (const channelKey of channelKeys) { ... }
```

两层 Key 独立管理：`GATEWAY_AUTH_TOKEN` 控制谁能调 CF Worker；`UPSTREAM_CHANNEL_KEYS` 是各第三方上游渠道的 Key 列表，每项对应一个渠道，新增或替换渠道只需修改此 Secret，不影响 NewAPI 侧配置。

### 6.3 计费单价换算示例

```
定价 0.02 元/张，积分单位 0.000001 元/积分
目标：每次生成扣 20,000 积分

配置：模型倍率 = 1，seconds = 1（CF Worker 固定返回），每秒单价 = 20000
计算：1 × 1 × 20000 = 20000 积分 ✅
```

---

## 七、协议转换规范

### 7.1 端点映射

| 客户端 / NewAPI 调 CF Worker | → | CF Worker 调真实上游 |
|---|---|---|
| POST /v1/videos | → | POST /v1beta/models/{model}:generateContent |
| GET /v1/videos/{id} | → | 查 CF KV，不调上游 |
| GET /v1/videos/{id}/content | → | 302 重定向至图片公网链接 |

`{model}` 由请求体的 `model` 字段直接替换，例：
```
model = "gemini-3-pro-image-preview"
  → POST /v1beta/models/gemini-3-pro-image-preview:generateContent
```

### 7.2 入站请求转换（客户端 JSON → Gemini generateContent）

**字段映射：**

| 客户端字段 | Gemini 请求位置 | 备注 |
|---|---|---|
| model | 端点路径 {model} | 不进入请求体 |
| prompt | contents[0].parts[0].text | 第一个 text part |
| aspect_ratio | generationConfig.imageConfig.aspectRatio | 直接透传 |
| image_size | generationConfig.imageConfig.imageSize | 直接透传 |
| image[n] | contents[0].parts[n+1].inlineData | URL 在 Workflow Step 1 内拉取转 base64 |

**image 字段处理（在 Workflow Step 1 内执行）：**
- URL 格式：fetch 拉取（超时 10s，大小 ≤ 10MB），转 base64，mimeType 从 Content-Type 取；拉取失败写入 failed，code: image_fetch_failed
- base64 格式：直接使用，mimeType 默认 image/png
- 超过 8 张：提交阶段 400 返回（格式校验，见 §8.1）

**完整转换示例：**
```
── 入站 ──────────────────────────────────────────────────────────────
POST /v1/videos
Authorization: Bearer {GATEWAY_AUTH_TOKEN}

{
  "model":        "gemini-3-pro-image-preview",
  "prompt":       "基于参考图中的产品，生成电商主图",
  "aspect_ratio": "16:9",
  "image_size":   "4K",
  "image": [
    "https://example.com/product.jpg",
    "data:image/png;base64,iVBORw0KGgo..."
  ]
}

── 出站（Step 1 拉取 URL 后构造）──────────────────────────────────────
POST /v1beta/models/gemini-3-pro-image-preview:generateContent
Authorization: Bearer {UPSTREAM_CHANNEL_KEYS[i]}   ← 当前尝试的渠道 Key

{
  "contents": [{
    "role": "user",
    "parts": [
      { "text": "基于参考图中的产品，生成电商主图" },
      { "inlineData": { "mimeType": "image/jpeg", "data": "<url拉取后的base64>" } },
      { "inlineData": { "mimeType": "image/png",  "data": "iVBORw0KGgo..." } }
    ]
  }],
  "generationConfig": {
    "imageConfig": { "aspectRatio": "16:9", "imageSize": "4K" },
    "responseModalities": ["TEXT", "IMAGE"]
  }
}
```

### 7.3 出站响应改写规则（Gemini → Sora 状态）

Gemini 响应结构为 `candidates[0].content.parts`，包含 text 和 inlineData 两类 part 混排。

**Part 提取：**
- `inlineData`：取 `data`（base64）和 `mimeType`，经图床处理得公网 URL
- `text`：拼接后存入 `description` 字段，**不可丢弃**（安全过滤时是唯一错误线索）

**单张图片确认：** Gemini 每次调用固定只生成一张图，parts 中有且仅有一个 inlineData。

**异常场景处理：**

| 场景 | 判断条件 | 处理方式 |
|---|---|---|
| 正常生图 | parts 中存在 inlineData | 提取 base64 → 上传图床 → 写入 succeeded |
| 安全过滤 | finishReason 为 SAFETY / IMAGE_SAFETY，无 inlineData | 写入 failed，error.message 取 text part 内容 |
| 无图无文 | parts 为空或无 inlineData 也无 text | 写入 failed，message = "upstream returned no image" |
| 图床全部失败 | uguu 超时 → R2 也失败 | 写入 failed，message = "image hosting failed" |

### 7.4 model 字段处理

`model` 直接透传进端点路径，不进入请求体，不做白名单校验。上游不支持该模型时由上游返回 4xx，由 Step 1 的错误处理逻辑（§5.7）负责处理。

```
model = "gemini-3-pro-image-preview"
  → POST /v1beta/models/gemini-3-pro-image-preview:generateContent

model = "gemini-2.5-flash-image"
  → POST /v1beta/models/gemini-2.5-flash-image:generateContent
```

---

## 八、生产级处理细则

### 8.1 入参校验

所有校验在触发 Workflow 前同步完成，校验失败直接返回 400，不消耗 Workflow 配额。参考图 URL 的实际拉取下移至 Workflow Step 1，提交阶段仅做格式合法性校验。

| 字段 | 校验规则 | 不合规处理 |
|---|---|---|
| prompt | 必填；长度 1 ～ 4000 字符 | 400，code: invalid_request |
| model | 必填；非空字符串，直接透传不做白名单限制 | 400（仅空值/缺失） |
| image | 非必填；数组最多 8 项；每项为合法 URL 格式或 base64 格式 | 400，code: invalid_request |
| aspect_ratio | 非必填；直接透传，不做枚举限制 | 不校验 |
| image_size | 非必填；限定枚举值（1K / 2K / 4K） | 400，code: invalid_request |

> 参考图 URL 的可达性、大小限制（≤ 10MB）、超时（10s）在 Workflow Step 1 内验证。拉取失败时写入 KV failed 状态，code: image_fetch_failed，NewAPI 轮询后自动退费。

### 8.2 CF KV 最终一致性边界

CF KV 为全球分布式最终一致性存储，写入后跨数据中心同步延迟通常为数秒，极端情况可达数十秒。

**已知风险点：** Workflow Step 3 写入 `succeeded` 后，若 NewAPI 轮询打到尚未同步的边缘节点，会读到 miss 或旧的 `processing` 状态。前者若处理不当（直接返回 404）会导致误退费。

**缓解策略：**
1. KV miss 时强制查询 Workflow 实例状态作为权威来源（见 §5.3），禁止跳过直接 404
2. Workflow 状态为 `complete` 但 KV miss 时，视为同步延迟，异步触发 KV 补写，并对本次轮询返回 `processing`，由 NewAPI 在下一轮轮询时读到已同步的 `succeeded`
3. KV `processing` 的 TTL（15 分钟）须 > Workflow 总超时（12 分钟），确保正常完成的任务在终态写入前不会因 TTL 过期导致穿透

### 8.3 uguu.se 熔断机制

| 状态 | 判断条件 | 行为 |
|---|---|---|
| 关闭（正常） | 连续失败次数 < 5 | 正常尝试 uguu，失败降级 R2 |
| 断开（熔断） | 连续失败次数 ≥ 5 | 直接走 R2，跳过 uguu，持续 5 分钟 |
| 半开（探测） | 熔断满 5 分钟后 | 放一次请求试探，成功则恢复 |

熔断状态存入 CF KV（key: `uguu:circuit`），value 包含失败次数和断开时间戳。

### 8.4 结构化日志规范

```json
{
  "ts":          "2026-03-18T10:00:00Z",
  "task_id":     "img_<uuid>",
  "event":       "task_submitted | step1_success | step1_failed | step2_success | step2_fallback_r2 | task_completed | task_failed",
  "model":       "gemini-3-pro-image-preview",
  "key_index":   0,
  "duration_ms": 1240,
  "image_host":  "uguu | r2",
  "error_code":  "RATE_LIMIT_EXCEEDED",
  "error_msg":   "..."
}
```

### 8.5 错误码规范

| error.code | 触发场景 | 客户端处理建议 |
|---|---|---|
| content_policy_violation | Gemini 安全过滤拦截 | 提示修改 prompt，不建议重试 |
| upstream_unavailable | 全部渠道配额耗尽或均不可用 | 提示稍后重试 |
| upstream_timeout | 上游超时，重试耗尽 | 提示稍后重试 |
| image_hosting_failed | uguu 和 R2 全部失败 | 提示用户联系支持 |
| image_fetch_failed | 参考图 URL 拉取失败 | 提示检查图片链接是否可访问 |
| invalid_request | 参数错误 | 提示检查请求参数 |
| unknown_error | 未分类异常 | 记录日志，提示联系支持 |

### 8.6 R2 存储 Key 命名规范

```
格式：images/{YYYY}/{MM}/{DD}/{task_id}.{ext}
示例：images/2026/03/18/img_a1b2c3d4.png

ext 从 Gemini 响应的 mimeType 推断（image/png → png，image/jpeg → jpg）
R2 Lifecycle Rule 按前缀 "images/" 配置 7 天自动删除
```

---

## 九、非功能需求

| 类别 | 要求 |
|---|---|
| 提交接口响应 | POST /v1/videos p99 ≤ 300ms（提交阶段不执行参考图拉取） |
| 任务总超时 | 12 分钟（Workflow 总超时） |
| KV TTL | processing = 15min；succeeded/failed = 7 天 |
| 图片有效期 | uguu 模式 3 小时；R2 模式 7 天 Lifecycle Rule |
| Key 安全 | UPSTREAM_CHANNEL_KEYS 通过 CF Worker Secret 注入，禁止明文出现在代码或配置中 |
| 向后兼容 | 文字模型链路零改动，NewAPI 现有渠道配置无需变更 |
| 容量规划 | CF Workflows 免费层约 33,000 次/月；超出按量计费；R2/Workers 出站流量均免费 |
| 可观测性 | 结构化日志接入 CF Logpush；关键指标：任务成功率、Step1 平均耗时、uguu 熔断次数 |

---

## 开发里程碑

| 阶段 | 任务 | 产出 |
|---|---|---|
| D1 | CF Worker 骨架；入参校验（格式层）；POST mock 返回 processing | 提交端点可用 |
| D2 | GET 轮询端点；KV 状态读写；Workflow 实例状态兜底逻辑；302 content 端点 | 完整轮询链路（含 KV 一致性保护） |
| D3 | CF Workflow；Step1 多渠道切换 + 参考图拉取完整逻辑；Step2 uguu/R2 双模式；结构化日志 | 端到端生图 |
| D4 | uguu 熔断；全错误类型覆盖测试；异常场景压测 | 健壮性验证 |
| D5 | NewAPI 配置 Sora 渠道；计费联调；多渠道配额耗尽场景压测 | 计费链路验证 |
| D6 | 灰度 10% 流量，观察 48h，全量切换 | 上线 |
