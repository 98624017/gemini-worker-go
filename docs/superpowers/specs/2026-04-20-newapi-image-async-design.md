# Newapi 前置异步入口与 Rust 上游改写设计

## 背景

当前仓库已经有两条稳定链路：

- `async-gateway`
  负责 Gemini 图片任务的异步受理、入库、排队、后台转发、状态查询、恢复扫描与清理。
- `rust-sync-proxy`
  负责 Gemini 同步代理改写，已经具备上游鉴权重写、请求图片物化、响应图片上传到图床或 R2、以及 usage 占位信息构造能力。

本次需求不是替换现有 Gemini 链路，而是在保留 Gemini 全量兼容的前提下，新增一条面向 Newapi / OpenAI 图片协议的异步入口，并复用已有异步任务机制与图片上传机制。

目标链路如下：

1. 客户端调用 `async-gateway` 的 `POST /v1/images/generations`
2. `async-gateway` 受理请求、落库任务、异步转发到 Newapi
3. Newapi 同步请求 `rust-sync-proxy` 的 `POST /v1/images/generations`
4. `rust-sync-proxy` 将请求改写为真实上游协议并发起同步请求
5. 真实上游返回 `b64_json`
6. `rust-sync-proxy` 将图片上传到图床或 R2，构造稳定 `usage`
7. `async-gateway` 将最终结果记录为任务成功结果
8. 客户端继续通过既有 `/v1/tasks/*` 查询任务状态

## 设计目标

- Gemini 现有异步提交、查询、管理页面与同步代理链路全部保持不变
- 新增 OpenAI / Newapi 图片协议入口，不污染 Gemini 协议
- 复用现有异步任务系统，而不是再造一套任务系统
- 复用现有 Rust 侧上传图床或 R2 的能力
- 对 Newapi 侧返回稳定的 `usage`，提高计费触发稳定性
- 对无 `data:image/...` 前缀的 `b64_json` 做稳健格式识别

## 非目标

- 不重构现有 Gemini 异步接口
- 不引入新的任务查询资源路径
- 不把所有图片协议抽象成大而全的多协议框架
- 不在首版支持文件上传、base64 输入、非 URL 引用图输入
- 不重做 Cloudflare task dashboard 的整体 UI

## 已确认约束

### Gemini 保持不变

- `POST /v1beta/models/{model}:generateContent` 保持原样
- Gemini 任务继续通过 `/v1/tasks/{id}` 和 `/v1/tasks/batch-get` 查询
- Gemini 成功态查询结果继续返回 `candidates`

### Newapi 图片异步入口

- 新增 `POST /v1/images/generations`
- 请求头：
  - `Authorization: Bearer <NEWAPI_API_KEY>`
- 请求体首版支持：
  - `model`
  - `prompt`
  - `response_format`
  - `image`
  - `images`
  - `reference_images`
- `response_format` 只接受 `url` 或缺省，网关内部统一归一为 `url`
- 引用图首版只接受 URL 数组
- 三个图片字段名视为同义词，内部统一标准化
- `model` 不写死为 `gpt-image-2`，但本次设计目标围绕 `gpt-image-2`

### 任务查询协议

- 新入口提交成功仍返回现有异步受理格式
- 继续使用既有 `/v1/tasks/{id}` 和 `/v1/tasks/batch-get`
- 任务对象继续使用 `object: "image.task"`
- Newapi 图片任务成功态新增 `result` 字段
- Gemini 任务成功态继续使用 `candidates`

### Rust 同步改写入口

- 新增 `POST /v1/images/generations`
- 请求头：
  - `Authorization: Bearer <baseurl|API_KEY>`
- 仅接受 `response_format=url` 或缺省
- 转发到真实上游时强制改写为：
  - `reference_images`
  - `response_format=b64_json`

### Usage 构造

- 返回固定常量值，不做动态估算
- 首版固定为：
  - `input_tokens = 1024`
  - `input_tokens_details.image_tokens = 1000`
  - `input_tokens_details.text_tokens = 24`
  - `output_tokens = 1024`
  - `total_tokens = 2048`
  - `output_tokens_details.image_tokens = 1024`
  - `output_tokens_details.text_tokens = 0`

### 上游 `b64_json` 图片格式识别

- 上游返回的 `b64_json` 没有 `data:image/png;base64,` 前缀时，不依赖 data URI
- Rust 侧先 base64 解码为原始字节
- 通过 magic bytes 嗅探图片类型
- 嗅探失败时兜底按 `image/png` 处理

## 总体方案

采用“双提交协议并存、单任务查询系统复用”的方案：

- `async-gateway` 同时支持 Gemini 与 Newapi 图片提交通道
- 所有任务继续落入同一套任务表、payload 表、队列、恢复扫描、清理与查询接口
- 任务记录新增轻量协议标记，用于在后台解析结果和在查询接口序列化成功态
- `rust-sync-proxy` 新增单独图片协议路由，不改现有 Gemini 路由
- Cloudflare task dashboard 保持代理与 KV 缓存语义不变，仅补前端双协议展示

该方案的核心收益是：

- 对 Gemini 零侵入
- 对异步任务体系最大复用
- 对 Rust 同步代理仅增加一条新路由与相应改写逻辑
- 后续若再接其他 OpenAI 图片模型，仍能复用该入口

## `async-gateway` 设计

### 路由层

新增路由：

- `POST /v1/images/generations`

保留原路由：

- `POST /v1beta/models/{model}:generateContent`
- `GET /v1/tasks/{id}`
- `POST /v1/tasks/batch-get`
- 其余管理与内容路由不变

路由行为：

- 根据请求路径分派到 Gemini submit handler 或 Newapi 图片 submit handler
- 查询路由仍统一走同一套 query handler

### 请求校验与标准化

新增独立校验器，例如 `internal/validation/image_generation_request.go`。

职责：

- 规范化 `Authorization`，复用现有 Bearer 解析逻辑
- 读取并解压请求体，复用现有 `gzip/br` 解压与体积限制策略
- 校验 `model` 为非空字符串
- 校验 `prompt` 为字符串，允许为空但不做额外改写
- 校验 `response_format`
  - 缺省时补成 `url`
  - 显式为 `url` 时通过
  - 其余值直接拒绝
- 接受三个同义字段：
  - `image`
  - `images`
  - `reference_images`
- 统一标准化成单一内部字段，例如 `reference_images`
- 要求引用图字段为 URL 数组
  - 每项必须是非空 `http/https` 绝对 URL
  - 不支持 base64 输入
  - 不支持文件上传
- 输出标准化 JSON，用于入库和后续转发

### 任务模型扩展

在现有任务模型基础上增加最小协议标识字段，例如：

- `request_protocol`
  - `gemini_generate_content`
  - `openai_image_generation`

该字段用途：

- worker 知道如何解析最终响应
- query handler 知道如何序列化成功结果
- 历史任务若没有该字段，按 Gemini 默认处理

除协议标识外，不新建任务表，不拆分任务表。

### 受理响应

新入口仍然返回现有异步受理格式：

```json
{
  "id": "img_xxx",
  "object": "image.task",
  "model": "gpt-image-2",
  "created_at": 1776663103,
  "status": "accepted",
  "polling_url": "/v1/tasks/img_xxx",
  "content_url": "/v1/tasks/img_xxx/content"
}
```

这样客户端能明确知道这是异步任务，而不是同步图片生成结果。

### 后台转发

继续复用现有 worker 与 forwarder：

- 入库时保存原始 path、query、标准化后的 body、转发头和加密后的 Authorization
- 后台转发时直接按任务原始 path 发往 Newapi 基座
- Newapi 图片任务的 path 为 `/v1/images/generations`
- forwarder 保持不理解业务协议，只负责超时、错误分类和发起请求

### 结果摘要

现有结果摘要只覆盖 Gemini 风格响应，本次需要扩为按协议解析。

建议扩展 `ResultSummary`，新增用于 OpenAI 图片结果的槽位，例如：

- `OpenAIImageResult`
  - `Created`
  - `Data`
  - `Usage`

Gemini 仍保留：

- `ImageURLs`
- `FinishReason`
- `ModelVersion`
- `ResponseID`
- `UsageMetadata`
- `TextSummary`

解析策略：

- Gemini 任务：继续解析 `candidates`
- Newapi 图片任务：解析
  - `created`
  - `data[].url`
  - `usage`

兼容要求：

- 即使 Newapi 图片任务最终对外通过 `result.data` 暴露图片 URL，内部仍要把这些 URL 同步镜像到 `ResultSummary.ImageURLs`
- 这样现有 `content_url` 重定向能力、任务列表摘要逻辑和可能依赖首图 URL 的旧代码都不需要额外分叉

### 查询序列化

`GET /v1/tasks/{id}` 与 `POST /v1/tasks/batch-get` 保持同一路径，但成功态按协议分支：

- Gemini 成功：
  - 输出既有 `candidates`
  - 可继续包含 `usage_metadata`
- Newapi 图片成功：
  - 输出 `result`

示例：

```json
{
  "id": "img_xxx",
  "object": "image.task",
  "model": "gpt-image-2",
  "created_at": 1776663103,
  "status": "succeeded",
  "result": {
    "created": 1776663103,
    "data": [
      {
        "url": "https://example.com/final.png"
      }
    ],
    "usage": {
      "input_tokens": 1024,
      "input_tokens_details": {
        "image_tokens": 1000,
        "text_tokens": 24
      },
      "output_tokens": 1024,
      "total_tokens": 2048,
      "output_tokens_details": {
        "image_tokens": 1024,
        "text_tokens": 0
      }
    }
  }
}
```

失败态与不确定态保持现有语义：

- `failed`
- `uncertain`
- `error`
- `transport_uncertain`

## `rust-sync-proxy` 设计

### 路由层

保留现有 Gemini 路由：

- `POST /v1beta/models/{model}:generateContent`

新增图片路由：

- `POST /v1/images/generations`

该图片路由使用独立 handler，不和 Gemini handler 混用分支。

### 上游鉴权

继续复用现有 `<baseurl|apiKey>` 解析能力。

支持：

- `Authorization: Bearer <apiKey>`
- `Authorization: Bearer <baseurl|apiKey>`

本次图片路由不要求额外新增鉴权格式。

### 请求标准化与改写

新图片 handler 接收到外部请求后，执行如下步骤：

1. 解析 JSON body
2. 校验 `response_format`
   - 缺省 -> 补成 `url`
   - `url` -> 通过
   - 其他值 -> `400`
3. 识别 `image / images / reference_images`
4. 统一标准化为 `reference_images`
5. 要求引用图为 URL 数组
6. 生成发往真实上游的新 body：
   - 保留原 `model`
   - 保留原 `prompt`
   - 写入 `reference_images`
   - 强制 `response_format = b64_json`
   - 不再向上游透传别名字段 `image` / `images`

### 上游响应改写

真实上游成功返回后，Rust 侧执行如下步骤：

1. 读取 `data[]`
2. 对每个元素提取 `b64_json`
3. 将 `b64_json` base64 解码为原始字节
4. 对字节做图片类型嗅探
   - PNG
   - JPEG
   - WEBP
   - GIF
   - 嗅探失败则按 `image/png`
5. 使用已有 uploader 上传到图床或 R2
6. 生成最终对外响应：

```json
{
  "created": 1776663103,
  "data": [
    {
      "url": "https://..."
    }
  ],
  "usage": {
    "input_tokens": 1024,
    "input_tokens_details": {
      "image_tokens": 1000,
      "text_tokens": 24
    },
    "output_tokens": 1024,
    "total_tokens": 2048,
    "output_tokens_details": {
      "image_tokens": 1024,
      "text_tokens": 0
    }
  }
}
```

`created` 取值规则：

- 优先使用真实上游返回的 `created`
- 若真实上游缺失该字段，则由 `rust-sync-proxy` 在响应改写时使用当前 UTC Unix 秒时间戳补齐
- `async-gateway` 不再自行二次推导 `result.created`

### 图片类型嗅探

实现要求：

- 不依赖 `data:image/...` 前缀
- 优先基于 magic bytes 识别 MIME
- MIME 决定：
  - 上传时 `Content-Type`
  - 上传文件扩展名
  - R2 对象元数据
- 若嗅探失败但解码成功，回退为 `image/png`
- 若 base64 解码失败，直接视为上游无效响应

### 与既有上传能力的关系

本次不新增新的上传后端。

继续复用已有：

- `legacy`
- `r2`
- `r2_then_legacy`

因此新图片路由只需接入已有 uploader 接口，不应复制上传逻辑。

### Admin 日志

图片新路由也要纳入已有 admin 日志体系，并新增清晰的错误阶段标记：

- `parse_request`
- `rewrite_request`
- `send_upstream_request`
- `decode_b64_json`
- `sniff_image_mime`
- `upload_output_image`
- `rewrite_response`

目的是在上游响应脏数据、图床上传失败或 MIME 识别异常时，能够快速定位阶段。

## Cloudflare task dashboard 设计

### Worker 代理层

`worker/api-proxy.ts` 与 `worker/index.ts` 不需要功能性改动。

原因：

- 代理层只透传 `/api/v1/*`
- 终态任务 KV 缓存只按 `status` 和 `owner_hash` 判断
- 不关心成功态内部是 `candidates` 还是 `result`

### 前端展示层

前端需要最小联动。

当前前端详情解析是 Gemini 专属：

- 图片从 `candidates[].content.parts[].inlineData.data` 提取
- 元数据从 `usage_metadata` 展示

新增后需要双协议解析：

- 若存在 `candidates`，按 Gemini 方式显示
- 若存在 `result.data`，按 Newapi 图片结果显示
- 元数据展示优先级：
  - Gemini：`usage_metadata`
  - Newapi 图片：`result.usage`

不重做 UI，只补充：

- API 类型定义
- 图片 URL 提取函数
- usage 提取函数
- 详情页渲染分支

## 错误处理

### `async-gateway`

新入口在任务受理前的错误：

- 缺少或非法 `Authorization` -> `401`
- 非法 JSON -> `400`
- `image/images/reference_images` 不是数组或成员不是 URL -> `400`
- `response_format` 非 `url` -> `400`
- 解压后体积超限 -> `413`

任务入库后的错误保持现有语义：

- 队列满 -> `503`
- 下游返回业务错误 -> 任务 `failed`
- 请求已发出但链路中断 -> 任务 `uncertain`

### `rust-sync-proxy`

图片新路由的错误分类：

- 请求体非法 -> `400`
- `<baseurl|apiKey>` 解析失败 -> `400` 或 `401`
- 上游请求超时 -> `504`
- 上游网络失败 -> `502`
- 上游成功返回但 `b64_json` 缺失 -> `502`
- `b64_json` base64 解码失败 -> `502`
- 上传图床或 R2 失败 -> `502`

错误响应不需要模拟 Gemini 风格，而应保持该图片路由自身的一致错误语义。

## 数据迁移

数据库只做增量 migration：

- 为任务表增加协议标记字段
- 默认值指向 Gemini 协议

约束：

- 不拆分任务表
- 不复制历史任务
- 不要求对老数据做语义迁移
- 老任务没有新字段时，也要按 Gemini 默认行为读取

payload 表保持不变，继续复用：

- 原始 path
- 原始 query
- 标准化后 body
- 转发头
- 加密后的 Authorization

## 上线顺序

推荐顺序：

1. 先落地 `rust-sync-proxy` 新图片路由并完成同步联调
2. 再落地 `async-gateway` 新异步入口与双成功态查询
3. 最后补 `cloudflare/task-dashboard` 前端双协议展示

这样能确保每一层都有明确的联调基线，问题定位更直接。

## 测试策略

### `async-gateway`

至少覆盖：

- 新路由识别
- 新请求校验
- 三种图片字段名归一化
- `response_format` 仅 `url` 或缺省通过
- 受理成功响应
- 任务入库后的协议标识正确
- 查询接口双成功态序列化
- batch-get 同时返回 Gemini 与 Newapi 图片任务时格式正确

### `rust-sync-proxy`

至少覆盖：

- 新图片路由 smoke test
- `<baseurl|apiKey>` 鉴权解析
- `image/images/reference_images` 归一化
- 请求改写为 `reference_images + b64_json`
- 单图与多图响应改写
- 固定 `usage` 构造
- PNG/JPEG/WEBP/GIF 嗅探
- 嗅探失败回退 PNG
- base64 非法响应
- 上传失败场景

### `cloudflare/task-dashboard`

至少覆盖：

- 老 Gemini 任务详情继续可展示
- 新 `result.data[].url` 可展示
- 新 `result.usage` 可展示
- 不影响现有列表查询与终态缓存

## 风险与缓解

### 风险 1：查询接口成功态多态

说明：

- `/v1/tasks/{id}` 成功态会同时存在 `candidates` 与 `result` 两种形态

缓解：

- 仅在成功态分支做协议区分
- 顶层资源模型与任务状态保持一致
- Cloudflare 前端补双解析函数

### 风险 2：上游返回脏 `b64_json`

说明：

- 可能缺字段、非法 base64、空字符串

缓解：

- 明确作为 `upstream_invalid_response`
- 不静默跳过
- Admin 日志记录失败阶段

### 风险 3：图片格式无法从前缀判断

说明：

- `b64_json` 常不带 data URI

缓解：

- 统一走 magic bytes 嗅探
- 失败时回退 PNG

### 风险 4：看板能查到任务但看不到图片

说明：

- 现有前端只懂 Gemini `candidates`

缓解：

- 将 task dashboard 前端补到本次交付范围
- Worker 代理层不动，前端解析层补双协议

## 验收标准

- Gemini 现有异步链路零回归
- `async-gateway` 可成功受理 `POST /v1/images/generations`
- `rust-sync-proxy` 可成功改写到真实上游并返回 URL 结果
- `async-gateway` 查询成功态可返回 `result.created + result.data + result.usage`
- 固定 `usage` 稳定返回
- 对无 MIME 前缀的 `b64_json` 可正确嗅探或回退
- task dashboard 能同时展示 Gemini 与 Newapi 图片任务

## 推荐实施顺序

1. `rust-sync-proxy` 新图片路由与响应上传
2. `async-gateway` 新入口、协议标记、结果双序列化
3. `cloudflare/task-dashboard` 双协议展示
4. 全链路回归测试与小流量验证

该顺序可以最大化隔离风险，并避免在异步层调试时同时面对同步代理和前端展示问题。
