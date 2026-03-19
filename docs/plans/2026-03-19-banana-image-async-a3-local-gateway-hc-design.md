# 香蕉Pro 图像生成异步化 A3-LocalGateway-HC 方案设计文档

**同机 Go 异步网关 · 内网长连接到 NewAPI · PostgreSQL 权威状态源 · 面向高并发设计**

涉及系统：客户端 · Go Async Gateway · PostgreSQL · NewAPI · `gemini-worker-go`（NewAPI 下游适配层）· 真实 Gemini 图像上游

---

## 一、背景与目标

### 1.1 A3 要解决的问题

在 `A2-CF` 方案中，客户端长连接问题已经被切掉，但仍保留一条公网长链路：

```text
CF Workflow ──公网长连接──▶ NewAPI
```

这条链路虽然比“客户端直接长连”稳定得多，但仍有两个天然短板：

- 它依然是跨公网链路
- 它依然受第三方云平台执行环境约束

如果你的 `Go` 中间层和 `NewAPI` 本来就部署在同一台机器，或者至少是同一个局域网络内，那么从纯稳定性角度看，更自然的解法是：

```text
客户端 ──短连接──▶ Go Async Gateway
Go Async Gateway ──同机内网 / Docker Network──▶ NewAPI
```

这能把“最长的等待”放在“最稳的链路”上。

### 1.2 为什么不是 SQLite 主线

如果只是做低并发原型，`SQLite` 可以用。  
但既然你已经明确提出“从一开始就考虑高并发”，那主线就不应该再押 `SQLite`。

原因很直接：

- `SQLite` 天然是单 writer 模型
- 适合读多写少、事务短小的中低负载
- 不适合把“高频状态更新 + 高频轮询 + 长期持久化”作为主场景

因此，本方案主线改为：

- **复用现有 PostgreSQL 服务实例**
- 但使用**独立数据库、独立用户、独立连接池预算**

### 1.3 本方案一句话定义

客户端请求不再先进 `NewAPI`，而是先进一个**同机 Go 异步网关**：

1. `Go Async Gateway` 立即返回 `task_id`
2. 后台 worker 保持与同机 `NewAPI` 的长连接
3. `PostgreSQL` 负责持久化任务状态
4. 客户端只轮询 `Go Async Gateway`

### 1.4 本方案核心价值

- **链路最短**
  - 客户端短连接
  - 服务端本机长连接
- **稳定性最好**
  - 同机或内网调用远比公网更稳
- **无需依赖 CF 工作流/状态产品**
  - 不需要 `Workflows`
  - 不需要 `DO`
  - 不需要 `R2`
- **协议最自然**
  - 对外可保持 Gemini 风格请求
  - 对内保持 path/query/body/auth 语义一致，只换目标 baseUrl
- **更适合作为高并发主线**
  - PostgreSQL 处理并发状态持久化
  - 后续可再叠加 Redis 或多实例扩展

### 1.5 目标与非目标

**目标：**

- 提供一个面向高并发设计的异步任务入口
- 消除客户端长连接依赖
- 对内仍复用 `NewAPI` 的同步鉴权与计费逻辑
- 允许与现有 PostgreSQL 服务实例共存
- 为后续多实例扩容预留空间
- 完整保留最近 `3 天` 用户可查询的任务状态与结果摘要，支撑手动查询与后续网页任务中心

**非目标：**

- 本期不要求改造 `NewAPI` 为异步任务系统
- 本期不实现回调通知
- 本期不实现任务取消
- 本期不要求引入 Redis
- 本期不要求跨机房部署
- 本期不做永久归档，只保留最近 `3 天` 用户可查询的任务摘要数据

---

## 二、整体架构

### 2.1 链路概览

```text
客户端
  │
  │ POST /v1beta/models/{model}:generateContent
  ▼
Go Async Gateway
  ├─ 轻校验
  ├─ 写 PostgreSQL 初始状态与短期恢复 payload
  ├─ 投递本地任务执行器
  └─ 立即返回 task_id

本地 Worker
  ├─ 读取任务摘要与短期恢复 payload
  ├─ 转发到 NewAPI
  ├─ 长连接等待 NewAPI 最终结果
  └─ 回写 PostgreSQL 终态摘要

NewAPI
  ├─ 鉴权 / 计费
  └─ 转发到 `gemini-worker-go`

`gemini-worker-go`
  ├─ 处理请求体里的 URL 型 `inlineData`
  ├─ 按现有逻辑做 `output=url`
  └─ 调真实 Gemini 图像上游

客户端
  │
  │ GET /v1/tasks/{id}
  ▼
Go Async Gateway
  └─ 读 PostgreSQL / 内存缓存返回状态
```

### 2.2 组件职责

| 组件 | 职责 | 是否新增 |
|---|---|---|
| 客户端 | 提交任务、轮询状态、获取结果 | 需要改造 |
| Go Async Gateway | 异步任务入口、状态查询、后台执行、转发 NewAPI | 新开发，独立 Go Docker 项目 |
| PostgreSQL | 权威持久化状态源 | 复用现有服务实例 |
| NewAPI | 鉴权、同步生图、预扣/成功扣/失败退 | 逻辑保留 |
| `gemini-worker-go` | NewAPI 下游适配层，负责 URL 型 `inlineData` → base64、现有 `output=url` 逻辑 | 复用现有服务 |
| 真实 Gemini 图像上游 | 最终图片生成 | 零改动 |

### 2.3 核心设计原则

1. **客户端永远不承担长连接**
2. **Go -> NewAPI 使用本机或内网地址**
3. **PostgreSQL 是权威状态源**
4. **所有终态只写一次**
5. **对内转发时保持 path/query/body/auth 语义一致，不追求 header/压缩字节完全一致**
6. **高并发轮询与最近任务列表查询不能让 PostgreSQL 成为单点热点**
7. **后续网页前端只调用 Go 网关，不直连 PostgreSQL**

### 2.4 为什么 PostgreSQL 可共用服务实例

你当前机器上已经有为 `NewAPI` 启动的 PostgreSQL 服务。  
我的建议是：

- **共用服务实例**
- **不共用业务数据库边界**

推荐方式：

- PostgreSQL 服务进程继续共用
- 为异步网关新建独立数据库，例如 `banana_async_gateway`
- 新建独立用户，例如 `banana_async_user`
- 独立连接池预算
- 独立迁移脚本

这样既减少新基础设施，又避免异步任务表和 `NewAPI` 账务/业务表混在一起。

### 2.5 为什么不建议直接混库混表

如果直接把异步任务表建进 `NewAPI` 现有业务库里，问题会变多：

- 连接池竞争更难控
- 迁移发布边界不清晰
- 任务轮询热点可能影响 `NewAPI` 账务表
- 后续拆分服务困难

所以推荐口径是：

- **同 PostgreSQL 服务实例**
- **不同数据库 / 用户 / 连接池**

### 2.6 后续网页部署口径

后续如果要做“用户输入自己的 `API Key`，查看最近 `3 天 task_id` 与最终图片链接”的网页：

- 前端可以部署在 `CF Pages`、`CF Worker`、你自己的静态站
- 页面只负责登录态输入、调用任务查询接口、展示结果
- 页面后端仍然是 `Go Async Gateway` 的 `/v1/tasks` 系列接口
- `PostgreSQL` 只作为网关内部状态源，不暴露给前端或网页脚本

### 2.7 部署拓扑

本方案默认口径：

- `A3 Async Gateway` 是**新增的独立 Go Docker 项目**
- 不与现有 `gemini-worker-go` 合并为同一二进制
- 不与 `NewAPI` 合并为同一进程
- 推荐与 `NewAPI`、`PostgreSQL`、`gemini-worker-go` 处于同一台机器或同一 Docker Network

推荐部署形态：

```text
客户端
  │
  ▼
Nginx / 内网入口
  ├─ async 域名或 async baseUrl  ──▶ A3 Async Gateway
  └─ 其他同步流量              ──▶ NewAPI

A3 Async Gateway
  ├─ http://newapi:3000              （同 Docker Network 服务名示例）
  └─ PostgreSQL

NewAPI
  └─ gemini-worker-go

gemini-worker-go
  └─ 真实 Gemini 图像上游
```

说明：

- 若运行在 Docker 中，文档中的“同机内网”优先理解为**同 Docker Network 服务地址**
- 只有使用 host network 或宿主机直连时，`127.0.0.1` 才天然成立
- 对客户端而言，A3 的区别仍然主要是 **baseUrl 不同**，而不是 path 不同
- 由于 A3 是独立 Go Docker 项目，数据库驱动、迁移工具等依赖可以独立选型，不受现有 `gemini-worker-go` 轻依赖约束

---

## 三、客户端异步任务协议规范

本方案对外继续尽量贴近当前项目的 Gemini 风格协议，避免重新发明一套大而全私有协议。

### 3.1 提交任务

```http
POST /v1beta/models/{model}:generateContent
Authorization: Bearer <newapi-user-api-key>
Content-Type: application/json
Content-Encoding: gzip | br | identity
```

#### 设计原则

- 路径继续保持 Gemini 风格
- `model` 继续走路径
- 请求体继续使用 `contents` / `generationConfig`
- 参考图继续允许 `inlineData.data=http(s)://...`
- 该入口天然就是异步入口，不再引入 `?async=1`
- 最终结果固定要求走 `output=url`
- 在 **A3 Async Gateway 的 baseUrl** 上，这个 path 只表示异步入口
- 在 `NewAPI` 原有 baseUrl 上，同路径仍可继续保留同步语义

#### 客户端请求示例

```http
POST /v1beta/models/gemini-3-pro-image-preview:generateContent
Authorization: Bearer sk-xxxxx
Content-Type: application/json
Content-Encoding: gzip
```

```json
{
  "contents": [
    {
      "role": "user",
      "parts": [
        {
          "text": "基于参考图中的产品，生成电商主图，背景纯净，强化商品主体"
        },
        {
          "inlineData": {
            "mimeType": "image/jpeg",
            "data": "https://example.com/input-a.jpg"
          }
        }
      ]
    }
  ],
  "generationConfig": {
    "imageConfig": {
      "aspectRatio": "16:9",
      "imageSize": "4K",
      "output": "url"
    }
  }
}
```

#### 立即响应

```http
202 Accepted
Content-Type: application/json
```

```json
{
  "id": "img_01JQLOCALABC123456789",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "status": "accepted",
  "polling_url": "/v1/tasks/img_01JQLOCALABC123456789",
  "content_url": "/v1/tasks/img_01JQLOCALABC123456789/content"
}
```

### 3.2 查询任务状态

```http
GET /v1/tasks/{task_id}
Authorization: Bearer <newapi-user-api-key>
```

#### 运行中

```json
{
  "id": "img_01JQLOCALABC123456789",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "status": "running"
}
```

#### 轮询约定

- `accepted / queued / running` 响应应返回 `Retry-After: 3` 或 `5`
- 客户端必须遵守 `Retry-After`，并额外加入 `0 ~ 1s` 随机抖动
- 服务端应按 `task_id + owner_hash + IP` 做轮询限频
- 若客户端轮询过快，返回 `429 Too Many Requests`
- `/v1/tasks/{id}/content` 只用于终态图片访问，不替代状态轮询

#### 成功

```json
{
  "id": "img_01JQLOCALABC123456789",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "finished_at": 1773964898,
  "status": "succeeded",
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "text": "已根据提示生成图片"
          },
          {
            "inlineData": {
              "mimeType": "image/png",
              "data": "https://your-public-domain.example/proxy/image?u=..."
            }
          }
        ]
      },
      "finishReason": "STOP"
    }
  ]
}
```

#### 失败

```json
{
  "id": "img_01JQLOCALABC123456789",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "finished_at": 1773964850,
  "status": "failed",
  "error": {
    "code": "upstream_timeout",
    "message": "newapi upstream request timed out"
  }
}
```

#### 不确定态

```json
{
  "id": "img_01JQLOCALABC123456789",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "finished_at": 1773964850,
  "status": "uncertain",
  "transport_uncertain": true,
  "error": {
    "code": "upstream_transport_uncertain",
    "message": "connection to newapi broke after request dispatch; task result may be uncertain"
  }
}
```

> 由于本方案仍复用 `NewAPI` 同步接口，因此理论上仍存在“请求已发出但结果不明”的极低概率场景。  
> 只是这条风险在“同机 / 内网长连接”下会显著低于公网链路。

### 3.3 查询最近任务列表

```http
GET /v1/tasks?days=3&limit=20
Authorization: Bearer <newapi-user-api-key>
```

#### 规则

- `days` 默认 `3`，最大只允许 `3`
- `limit` 默认 `20`，最大建议 `100`
- 支持 keyset 分页参数：`before_created_at` + `before_id`
- 只返回当前 `Authorization` 对应用户自己的任务
- 默认按 `created_at DESC` 排序
- 该接口是后续网页任务中心的标准后端入口

推荐分页请求示例：

```http
GET /v1/tasks?days=3&limit=20&before_created_at=1773964700&before_id=img_01JQLOCALXYZ123456789
```

#### 响应示例

```json
{
  "object": "list",
  "days": 3,
  "items": [
    {
      "id": "img_01JQLOCALABC123456789",
      "model": "gemini-3-pro-image-preview",
      "status": "succeeded",
      "created_at": 1773964800,
      "finished_at": 1773964898,
      "content_url": "/v1/tasks/img_01JQLOCALABC123456789/content"
    },
    {
      "id": "img_01JQLOCALXYZ123456789",
      "model": "gemini-3-pro-image-preview",
      "status": "running",
      "created_at": 1773964700
    },
    {
      "id": "img_01JQLOCALERR123456789",
      "model": "gemini-3-pro-image-preview",
      "status": "uncertain",
      "created_at": 1773964600,
      "finished_at": 1773964650
    }
  ]
}
```

### 3.4 获取图片内容（可选）

```http
GET /v1/tasks/{task_id}/content
Authorization: Bearer <newapi-user-api-key>
```

行为：

- 成功：`302` 跳转到最终图片 URL
- 未完成：`409`
- 失败：`409`
- 不确定：`409`
- 不存在：`404`
- 非任务归属用户：`403`

说明：

- 该端点适合前端在任务成功后直接取图
- 加载中状态仍应轮询 `/v1/tasks/{id}` 或 `/v1/tasks`
- 响应应补 `Referrer-Policy: no-referrer`，尽量降低签名 URL 经 `Referer` 外泄的风险

---

## 四、计费设计

### 4.1 计费所有权不迁移

与 A2 一样，本方案不把计费搬到中间层：

- `Go Async Gateway` 不维护余额
- `Go Async Gateway` 不计算费用
- `Go Async Gateway` 不做扣费补偿
- 真正计费仍由 `NewAPI` 完成

### 4.2 计费全链路

| 阶段 | 执行方 | 说明 |
|---|---|---|
| 用户发起请求 | 客户端 | 带自己的 `NewAPI API Key` 调 Go 网关 |
| 任务受理 | Go Async Gateway | 仅创建任务，不计费 |
| 同步生图调用 | Go Worker → NewAPI | 透传用户 `API Key` 发起真实请求 |
| 预扣 / 成功扣 / 失败退 | NewAPI | 继续走当前已验证链路 |
| 状态回传 | Go Async Gateway | 仅保存并返回任务状态 |

### 4.3 与 A2 相比的计费风险变化

两者都存在“请求已发出但结果不明”的理论风险。  
但 A3 的链路更短：

```text
Go Async Gateway ──同机内网 / Docker Network──▶ NewAPI
```

因此相较 `A2-CF`：

- 网络不确定性更低
- 超时边界更可控
- 日志追踪更直接
- 出现 `upstream_transport_uncertain` 的概率应显著更低

---

## 五、Go Async Gateway 详细功能需求

### 5.1 端点实现清单

| 端点 | 方法 | 说明 |
|---|---|---|
| `/v1beta/models/{model}:generateContent` | `POST` | 提交异步任务 |
| `/v1/tasks/{id}` | `GET` | 查询任务状态 |
| `/v1/tasks` | `GET` | 查询最近 `3 天` 任务列表 |
| `/v1/tasks/{id}/content` | `GET` | 跳转到最终图片 URL |

### 5.2 提交端点处理流程

```text
1. 校验 Authorization，缺失返回 401
2. 校验 output 最终解析为 url，否则返回 400
3. 读取并解压请求体（gzip / br / identity）
4. 解析 JSON，按 Gemini 风格做格式校验
5. 计算解压后 body 大小
6. 生成 task_id
7. 派生 owner_hash
8. 写入 `tasks` 任务摘要
9. 写入 `task_payloads` 短期恢复 payload
10. 将任务投递到本地 worker 队列，并推进到 `status=queued`
11. 若队列已满：更新 `tasks.status=failed`、`error_code=queue_full`，并返回 `503 + Retry-After`
12. 返回 202 Accepted
```

#### 关键要求

- 提交接口不能等待生图完成
- 提交接口不能在主路径抓取参考图 URL
- 提交接口不能在主路径直接调用 `NewAPI`
- 参考图 URL 允许原样通过 `NewAPI` 继续下沉到 `gemini-worker-go` 处理
- 提交阶段的“解压 + JSON 解析”只服务于入参校验、大小控制、规范化转发与短期恢复，不代表要长保留原始大请求体
- 任务先落 PostgreSQL，再投递 worker，避免“已返回 task_id 但无状态记录”
- 只有任务受理记录成功提交后，才能返回 `202 + task_id`
- `tasks` 只保留用户查询与终态摘要
- `task_payloads` 只保留恢复重放所需的短期数据

#### `owner_hash` 归属策略

首版建议口径：

- `owner_hash = HMAC-SHA256(server_secret, normalized_api_key)`
- `server_secret` 必须来自稳定持久化配置，例如 `OWNER_HASH_SECRET`
- `normalized_api_key` 只取归一化后的 Bearer Token，不直接使用完整 `Authorization` 头原文
- `owner_hash` 只用于归属校验与最近 `3 天` 历史查询，不参与计费
- 日志、指标、数据库中都不得保存明文 API Key

首版接受的产品取舍：

- 若用户在 `NewAPI` 中重置或更换 API Key，则新旧 `owner_hash` 会变化
- 旧 Key 提交的最近 `3 天` 任务，默认不再能被新 Key 查询到

后续演进方向：

- 如果 `NewAPI` 后续能提供稳定 `user_id` 或等价主体标识
- 则可把 `owner_hash` 的输入从 `api_key` 切换为稳定主体标识
- 那时才能从根本上解决“换 Key 后历史记录断裂”的问题

### 5.3 查询端点处理流程

```text
1. 校验 Authorization
2. 派生 owner_hash
3. 做轮询限频，超限返回 429 + Retry-After
4. 优先查内存热点缓存
5. 缓存未命中则查 PostgreSQL
6. 若任务不存在 → 404
7. 若 owner_hash 不匹配 → 403
8. 若状态为 accepted / queued / running，则附带 Retry-After
9. 返回任务状态
```

### 5.4 最近任务列表端点处理流程

```text
1. 校验 Authorization
2. 派生 owner_hash
3. 做查询限频，超限返回 429 + Retry-After
4. 归一化 days / limit，days 最大只允许 3
5. 读取 before_created_at / before_id 游标
6. 优先查短时热点缓存
7. 缓存未命中则查 PostgreSQL 最近 3 天任务
8. 仅返回当前 owner_hash 的简版任务摘要
```

> 后续网页任务中心也应走这个端点，而不是直连 PostgreSQL。

### 5.5 content 端点处理流程

```text
1. 校验 Authorization
2. 校验任务归属
3. 该端点不承担加载态轮询语义
4. 从 `result_summary_json.image_urls[0]` 读取第一张终态图片 URL
5. 未完成 → 409
6. 不确定 → 409
7. 返回 302 跳转
```

### 5.6 本地 worker 执行模型

高并发前提下，不建议“每个请求直接起一个 goroutine 无限放飞”。

推荐：

- 固定大小 worker pool
- 每个 worker 处理一个长任务
- 并发度由配置控制
- 队列满时返回 `503 Service Unavailable` + `Retry-After`
- 错误码使用 `queue_full`

建议配置项：

- `MAX_INFLIGHT_TASKS`
- `MAX_QUEUE_SIZE`
- `TASK_POLL_RETRY_AFTER_SEC`
- `TASK_POLL_MIN_INTERVAL_MS`
- `POSTGRES_MAX_OPEN_CONNS`
- `POSTGRES_MAX_IDLE_CONNS`
- `NEWAPI_REQUEST_TIMEOUT_MS`
- `OWNER_HASH_SECRET`
- `TASK_PAYLOAD_ENCRYPTION_KEY`
- `SHUTDOWN_GRACE_PERIOD_SEC`
- `TTL_DELETE_BATCH_SIZE`

### 5.7 状态机

```text
accepted
  ↓
queued
  ↓
running
  ├─→ succeeded
  ├─→ failed
  └─→ uncertain

accepted
  └─→ failed(queue_full / persist_failed)
```

状态说明：

- `accepted`：已写库，待入执行队列
- `queued`：已进入 worker 队列，等待实际执行
- `running`：worker 已接管任务，正在调用或准备调用 `NewAPI`
- `succeeded`：成功终态
- `failed`：失败终态
- `uncertain`：请求可能已送达 `NewAPI`，但网关无法确认最终结果的终态

### 5.8 Worker 执行步骤与超时预算

**总超时建议：12 分钟**

#### Step 1：取任务并标记 `running`

| 项 | 说明 |
|---|---|
| 目标 | 从队列取出任务并写入 `running / worker_id / heartbeat_at` |
| 超时 | `5s` |
| 失败处理 | 重试 2 次；仍失败则记录告警 |

#### Step 2：转发请求到 NewAPI

| 项 | 说明 |
|---|---|
| 目标 | 发起本机 / 内网长连接同步请求，并在确认请求已写出后记录 `request_dispatched_at` |
| 超时 | `11 min` |
| 失败处理 | 见 §5.10 |

#### Step 3：写回终态

| 项 | 说明 |
|---|---|
| 目标 | 保存成功结果或失败信息 |
| 超时 | `10s` |
| 失败处理 | 重试 3 次；仍失败告警 |

#### Step 4：长等待期间刷新心跳

| 项 | 说明 |
|---|---|
| 目标 | 在等待 `NewAPI` 长连接返回期间定期刷新 `heartbeat_at` |
| 周期 | 建议 `15s ~ 30s` |
| 失败处理 | 连续失败告警；由恢复扫描接手 |

补充约束：

- 孤儿任务判定阈值必须显著大于心跳周期
- 首版建议：`heartbeat_stale_threshold >= 5m`
- 不得因为单次心跳写库失败就直接把任务转入 `uncertain`

### 5.9 网关重启恢复与优雅停机语义

边界目标：

- **网关重启后，已返回给用户的 `task_id` 不能丢**
- **对可能已经送达上游的任务，不能做盲目自动重试**

启动恢复规则：

1. 提交接口只有在 `tasks` 记录成功提交后，才返回 `202 + task_id`
2. 网关启动后执行一次恢复扫描
3. `status in (accepted, queued)` 的任务直接重新入队
4. `status = running` 且 `request_dispatched_at IS NULL` 的任务，视为“尚未真正发出”，可重新入队
5. `status = running` 且 `request_dispatched_at IS NOT NULL` 的任务，不自动重试，直接标记为 `uncertain`
6. `heartbeat_at` 长时间未刷新且无终态的 `running` 任务，也按上面规则做恢复判定
7. 若需要重放但缺少 `task_payloads`，则标记 `failed + recovery_payload_missing`

实现建议：

- `worker_id` 用于标识当前执行者
- `heartbeat_at` 用于识别孤儿任务
- `request_dispatched_at` 应通过 `httptrace.WroteRequest` 或等价机制，在确认请求已写出后再落库
- 恢复扫描优先处理 `accepted / queued / running` 三类非终态任务
- `WroteRequest` 到数据库成功写入 `request_dispatched_at` 之间，存在极小重放窗口；首版接受该概率风险

#### 优雅停机规则

目标：

- 发版或重启时尽量让已开始的长任务自然完成
- 仅在退出截止时间到达后，才把无法确认的在途任务改为 `uncertain`
- 避免“一收到 `SIGTERM` 就立刻把所有运行中任务改终态”

建议流程：

1. 拦截 `SIGTERM / SIGINT`
2. 立即停止接收新的提交请求，`POST /v1beta/models/{model}:generateContent` 返回 `503` + `Retry-After`
3. 已有查询接口可在短暂 drain 窗口内继续服务
4. 等待当前 worker 在 `SHUTDOWN_GRACE_PERIOD` 内尽量自然完成
5. 到达退出截止时间后，仍未完成且 `request_dispatched_at IS NOT NULL` 的任务，条件更新为 `uncertain`
6. 条件更新必须带 `WHERE status = 'running' AND finished_at IS NULL`，避免覆盖已经成功写回的终态

推荐补充信息：

- 对因停机被迫转入不确定态的任务，建议写 `error.code = gateway_shutdown_uncertain`
- 同时打结构化日志事件，例如 `shutdown_drain_started`、`shutdown_force_uncertain`
- 若任务尚未 `request_dispatched_at`，则不应直接标记 `uncertain`，而应在下次启动时按恢复扫描逻辑重新入队

### 5.10 Step 2 的重试与不确定态规则

原则与 A2 类似，但由于是本机 / 内网链路，允许稍微乐观一点。

#### 可安全重试

- DNS 失败（若走域名）
- TCP 建连失败
- 连接在请求发出前失败
- `NewAPI` 返回 `408`

#### 禁止自动重试

- 请求已经发给 `NewAPI`
- 读取结果前连接断开
- 已经接收到部分响应
- 接近源站超时边界的读超时
- 网关进入停机尾声，且任务已经 `request_dispatched_at`
- `NewAPI` 已返回明确业务错误（4xx / 5xx）

#### 不确定态处理

若请求可能已送达 `NewAPI`，但结果无法确认：

- `status = uncertain`
- `error.code = upstream_transport_uncertain`
- `transport_uncertain = true`

#### `NewAPI` HTTP 响应分类

| `NewAPI` 状态 | 首版处理 |
|---|---|
| `200` | 正常解析响应并写终态 |
| `400` / `404` / `422` | `failed + invalid_request/upstream_error` |
| `401` / `403` | `failed + auth_failed` |
| `402` | `failed + insufficient_balance` |
| `408` | 可安全重试一次；仍失败则 `failed + upstream_timeout` |
| `429` | 首版不自动重试，`failed + upstream_rate_limited` |
| `500` / `502` / `503` / `504` | 首版不自动重试，`failed + upstream_error` |

### 5.11 对内转发规则

本方案最关键的一条：

- **原始请求路径保持不变**
- **原始 query 保持不变**
- **原始 JSON body 语义保持不变**
- **原始 Authorization 主体保持不变**
- **请求体允许以规范化 JSON 重新编码后转发**
- **只替换 baseUrl 为 NewAPI 的内网地址**
- **若提交阶段已解压，则转发时必须去掉 `Content-Encoding` 头**
- **A3 worker 不重复实现 `gemini-worker-go` 的 URL 拉取、base64 转换、图床上传与响应图片整理逻辑**

示例：

```text
客户端请求：
POST http://gateway:8788/v1beta/models/gemini-3-pro-image-preview:generateContent?output=url

Worker 转发：
POST http://newapi:3000/v1beta/models/gemini-3-pro-image-preview:generateContent?output=url
```

建议额外透传：

- `X-Banana-Task-ID`
- `X-Request-ID`
- `X-Banana-Async-Source: local-gateway`

### 5.12 超时梯度约束

必须显式校准整条链路的超时顺序：

```text
入口层/Nginx 超时
  > Go Gateway -> NewAPI 超时
  > NewAPI -> `gemini-worker-go` / 上游链路超时
  > 上游厂商实际超时
```

推荐示意值：

- 上游实际超时：`10m`
- `NewAPI -> 上游`：`10m 30s`
- `Go Gateway -> NewAPI`：`11m`
- 入口层 / Nginx：`11m 30s ~ 12m`

若这个顺序被打乱，就会出现：

- 外层还在等待，里层其实早已超时
- 网关错误地长时间挂起
- 不必要的 `uncertain` 或错误告警

### 5.13 内存热点缓存设计

首版建议采用简单、可预测的本地内存缓存：

- **任务状态缓存**
  - key：`task_id`
  - value：任务当前状态快照
  - 更新方式：worker 每次状态写库成功后同步写缓存
- **列表查询缓存**
  - key：`owner_hash + days + limit + before_created_at + before_id`
  - value：最近任务列表摘要
  - 更新方式：短 TTL 自动失效

首版建议策略：

- `accepted / queued / running`：TTL `3 ~ 5s`
- `succeeded / failed / uncertain`：TTL `30 ~ 60s`
- 列表接口缓存：TTL `3 ~ 10s`
- 容量上限：建议先从 `10000` 条或固定内存预算起步
- 实现形态：优先使用分片内存 map + TTL；容量触顶时先清过期项，再淘汰最老的终态项
- 首版不要求为了缓存单独引入 Redis 或复杂分布式一致性方案

注意：

- 缓存是抗轮询热点的优化层，不是权威状态源
- 所有 miss 或缓存失效都必须可回退到 PostgreSQL
- 如果任务进入终态并完成数据库写回，缓存可短时保留，覆盖用户最后几轮轮询

---

## 六、PostgreSQL 持久化设计

### 6.1 复用方式

推荐：

- 复用现有 PostgreSQL 服务实例
- 新建独立数据库：`banana_async_gateway`
- 新建独立用户：`banana_async_user`
- 独立连接池参数

不推荐：

- 与 `NewAPI` 账务业务表混在同一数据库 schema 中
- 共享同一应用用户
- 不设连接预算直接放量

### 6.2 表设计

#### `tasks`

```sql
CREATE TABLE tasks (
  task_id              TEXT PRIMARY KEY,
  status               TEXT NOT NULL CHECK (status IN ('accepted', 'queued', 'running', 'succeeded', 'failed', 'uncertain')),
  model                TEXT NOT NULL,
  owner_hash           TEXT NOT NULL,
  request_path         TEXT NOT NULL,
  request_query        TEXT NOT NULL DEFAULT '',
  worker_id            TEXT NOT NULL DEFAULT '',
  heartbeat_at         TIMESTAMPTZ,
  request_dispatched_at TIMESTAMPTZ,
  result_summary_json  JSONB NOT NULL DEFAULT '{}'::jsonb,
  error_code           TEXT NOT NULL DEFAULT '',
  error_message        TEXT NOT NULL DEFAULT '',
  transport_uncertain  BOOLEAN NOT NULL DEFAULT FALSE,
  created_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  finished_at          TIMESTAMPTZ
);
```

#### `task_payloads`

```sql
CREATE TABLE task_payloads (
  task_id                   TEXT PRIMARY KEY REFERENCES tasks(task_id) ON DELETE CASCADE,
  request_body_json         JSONB NOT NULL,
  forward_headers_json      JSONB NOT NULL DEFAULT '{}'::jsonb,
  auth_ciphertext           BYTEA NOT NULL,
  payload_expires_at        TIMESTAMPTZ NOT NULL,
  created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

设计口径：

- `tasks` 是**最近 `3 天` 用户可查询的长保留摘要表**
- `task_payloads` 是**只服务于恢复重放的短保留表**
- `Authorization` 不进入 `tasks`
- `Authorization` 若必须持久化，也只能以 `auth_ciphertext` 形式进入 `task_payloads`
- `auth_ciphertext` 必须使用稳定持久化的 `TASK_PAYLOAD_ENCRYPTION_KEY` 加密；进程重启或发版后该密钥不得变化
- `request_body_json` 保存的是**解压并规范化后的 JSON 请求体**，不是压缩前原始字节流
- `forward_headers_json` 只保留必要的非敏感转发头，例如 `Content-Type`、`Accept`、`X-Request-ID`
- `forward_headers_json` 不保存 `Authorization`、`Content-Encoding`、`Content-Length`、`Host`

#### 为什么仍需要短期 `task_payloads`

保留 `task_payloads` 不是为了让用户三天后还能看见完整请求体，而是只为了解决下面这个恢复语义：

- 网关已经给用户返回了 `task_id`
- 但进程在真正转发到 `NewAPI` 前异常退出
- 重启恢复扫描后，需要把这批“尚未真正发出”的任务重新入队

因此：

- 没有 `task_payloads`，就无法安全重放这类任务
- 有了 `request_dispatched_at` 后，任务一旦确认已发出，就不能再盲目自动重放
- 所以 `task_payloads` 只需要短期存在，服务恢复语义，不服务用户历史查询

#### `result_summary_json` 建议结构

```json
{
  "image_urls": ["https://n.uguu.se/VsRoGBTX.jpg"],
  "finish_reason": "STOP",
  "model_version": "gemini-3-pro-image-preview",
  "response_id": "Tki7aZiwEc2IjuMPv5yTwAI",
  "usage_metadata": {
    "prompt_token_count": 1010,
    "candidates_token_count": 1363,
    "total_token_count": 2592
  },
  "text_summary": ""
}
```

说明：

- 用户最近 `3 天` 查询主要依赖 `result_summary_json`
- 成功场景只需要保留图片 URL、必要元数据和少量文本摘要
- 失败场景主要依赖 `error_code + error_message`
- 不要求把完整 Gemini 原始响应体长保留在 `tasks`

#### 终态摘要提取规则

首版建议从 `NewAPI` 最终响应中只提取“用户查询真正需要的摘要”：

- 成功场景：
  - 从 `candidates[].content.parts[]` 中提取 `inlineData.data`
  - 在本方案前提下，`output=url` 已由下游链路处理完成，这里的 `inlineData.data` 应是最终图片 URL，而不是 base64
  - 同时提取 `mimeType`、`finishReason`、`responseId`、`modelVersion`、`usageMetadata`
- 安全过滤或无图失败：
  - 若 `finishReason` 为 `SAFETY / IMAGE_SAFETY`，或 `parts` 中无图片但有文本说明
  - 则把文本 part 内容优先提炼为 `error_message`
- 无图无文：
  - 记为 `failed`
  - `error_message` 可统一归纳为 `upstream returned no image`

推荐原则：

- 成功时把最终图片 URL 放入 `result_summary_json.image_urls`
- 失败时优先沉淀可读失败原因，而不是保存整份原始响应
- `result_summary_json.text_summary` 只保留少量必要文本，不做长篇原文归档

#### 为什么不长保留完整请求体 / 完整响应体

这不是当前产品需求，反而会引入两个额外成本：

- **存储膨胀**
  - 即使现在主路径要求 `output=url`，长时间保留完整 JSON 仍会放大 `JSONB/TOAST` 体积
- **敏感面扩大**
  - 完整请求和响应更容易夹带不必要的用户输入、头信息或调试字段

因此首版明确采用：

- `tasks`：只存最近 `3 天` 用户查询需要的摘要面
- `task_payloads`：只存恢复重放需要的短期内部数据面
- 不把“保存完整请求体 / 完整 Gemini 响应体”作为用户查询功能的一部分

#### `task_events`（可选，首版建议不建）

如果首版目标是尽快稳定上线，那么只靠 `tasks` 表已经足够。  
`task_events` 更偏向排障审计增强项，可以第二阶段再加。

```sql
CREATE TABLE task_events (
  id          BIGSERIAL PRIMARY KEY,
  task_id      TEXT NOT NULL,
  event_type   TEXT NOT NULL,
  payload_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

### 6.3 索引建议

```sql
CREATE INDEX idx_tasks_owner_created_at ON tasks(owner_hash, created_at DESC, task_id DESC);
CREATE INDEX idx_tasks_status_created_at ON tasks(status, created_at DESC);
CREATE INDEX idx_tasks_recovery_scan ON tasks(status, request_dispatched_at, heartbeat_at)
WHERE status IN ('accepted', 'queued', 'running');
CREATE INDEX idx_tasks_gc_finished_at ON tasks(finished_at)
WHERE status IN ('succeeded', 'failed', 'uncertain');
CREATE INDEX idx_task_payloads_gc_expires_at ON task_payloads(payload_expires_at);
```

若二阶段启用 `task_events`，再额外补：

```sql
CREATE INDEX idx_task_events_task_id_created_at ON task_events(task_id, created_at DESC);
```

若后续轮询非常高频，可考虑：

- 仅对运行中任务建立 partial index
- 成功任务短时间命中内存缓存，减少数据库读压
- 若列表接口字段稳定，再评估是否增加 `INCLUDE` 列做覆盖索引

### 6.4 `updated_at` 更新约束

首版口径：

- 不强制依赖数据库 trigger
- 由应用层所有 `UPDATE` 语句显式写 `updated_at = NOW()`
- 任何状态流转、payload 更新时间、恢复扫描写回都必须同步更新 `updated_at`

### 6.5 高并发下的数据库热点

真正压 PostgreSQL 的通常不是“写一个任务状态”，而是：

- 大量客户端高频轮询
- 网页任务中心频繁刷新最近任务列表
- 每次轮询都直接查库

因此高并发优化重点不是“换掉 PostgreSQL”，而是：

1. **热点状态走内存缓存**
2. **终态结果可短时缓存**
3. **轮询频率要控制**
4. **查询语句必须简单并命中索引**

### 6.6 连接池建议

建议初始值：

- `max_open_conns = 20 ~ 40`
- `max_idle_conns = 10 ~ 20`
- `conn_max_lifetime = 30m`

具体要结合：

- 机器 CPU
- PostgreSQL `max_connections`
- `NewAPI` 现有连接占用
- 实际轮询 QPS

### 6.7 PostgreSQL 是否会吃力

如果设计得当，**PostgreSQL 比 SQLite 更适合作为高并发主线**。

但它仍会吃力于以下情况：

- 轮询频率过高
- 每次轮询都打数据库
- 长事务
- 缺索引
- 长保留表里堆积过大的 JSON 列
- 连接池放太大

所以核心策略不是“只靠 PostgreSQL 硬扛”，而是：

- PostgreSQL 做权威持久化
- 运行中热点状态加本地内存缓存
- 必要时后续再加 Redis

---

## 七、生产级处理细则

### 7.1 入参校验

| 项目 | 规则 | 不合规处理 |
|---|---|---|
| `Authorization` | 必填 | `401` |
| `output` | 最终解析结果必须为 `url` | `400` |
| `{model}` | 必填，非空 | `400` |
| 请求体 | 合法 JSON | `400` |
| prompt 文本 | 建议 `1 ～ 4000` 字 | `400` |
| 参考图数量 | 最多 `8` 张 | `400` |
| 单 URL 长度 | 建议 `<= 4096` 字符 | `400` |
| 参考图协议 | 仅 `http / https` | `400` |
| 解压后请求体大小 | 建议硬上限 `2MB` | `413` |

补充说明：

- 在当前口径下，请求体里的图片输入应以 URL 形式出现，而不是大段 base64
- prompt 若控制在 `4000` 字以内，正常请求通常远低于 `2MB`
- 因此 `2MB` 更像防滥用与异常输入保护上限，而不是日常目标尺寸

### 7.2 压缩策略

- 默认：`gzip`
- 可选：`br`
- 兼容：`identity`

与 A2 不同，本方案**不需要**为 Workflow payload 做 `800KB` 分流。  
这里的压缩主要只是为了：

- 降客户端到网关的带宽
- 提高弱网提交成功率

### 7.3 结构化日志规范

```json
{
  "ts": "2026-03-19T10:00:00Z",
  "task_id": "img_01JQLOCALABC123456789",
  "event": "task_accepted | task_queued | task_running | newapi_call_started | newapi_call_succeeded | newapi_call_failed | task_succeeded | task_failed | task_uncertain | recovery_requeued | recovery_marked_uncertain | shutdown_drain_started | shutdown_force_uncertain",
  "model": "gemini-3-pro-image-preview",
  "duration_ms": 1240,
  "transport_uncertain": false,
  "error_code": "",
  "error_msg": ""
}
```

### 7.4 错误码规范

| `error.code` | 场景 |
|---|---|
| `invalid_request` | 参数不合法 |
| `missing_api_key` | 缺少用户 API Key |
| `request_too_large` | 解压后请求体过大 |
| `rate_limited` | 轮询或查询过快被限流 |
| `service_draining` | 网关处于优雅停机阶段，暂不接新任务 |
| `queue_full` | 本地队列已满 |
| `auth_failed` | `NewAPI` 鉴权失败 |
| `insufficient_balance` | `NewAPI` 明确返回余额不足 |
| `upstream_rate_limited` | `NewAPI` 或其下游触发限流 |
| `upstream_error` | `NewAPI` 明确返回业务错误 |
| `upstream_timeout` | `NewAPI` 超时 |
| `upstream_transport_uncertain` | 请求发出后链路异常中断 |
| `gateway_shutdown_uncertain` | 停机截止时仍无法确认最终结果 |
| `recovery_payload_missing` | 恢复扫描时缺少重放所需 payload |
| `persist_failed` | 数据库终态写入失败 |
| `unknown_error` | 未分类异常 |

### 7.5 TTL 与清理策略

建议：

- `tasks` 表完整保留最近 `3 天` 任务状态与结果摘要
- `task_payloads` 只做短期保留，不参与最近 `3 天` 用户查询
- `task_events` 若启用，也只保留最近 `3 天`
- 仅清理已完成终态且超出 `3 天` 的记录
- 仍在 `accepted / queued / running` 的任务不得被 TTL 线程误删

首版建议：

- 由 Go 网关中的单例后台清理器执行**小批量删除**
- 每隔 `1 ~ 5` 分钟删除一小批，例如 `500 ~ 1000` 行
- 以 `finished_at` 为准清理 `succeeded / failed / uncertain`，避免大事务和 WAL 尖峰
- `task_payloads` 在任务已 `request_dispatched_at` 或进入终态后，可按更短 TTL 清理
- `accepted / queued / running(request_dispatched_at IS NULL)` 的 payload 绝不能被过早删除

建议 SQL 形态：

```sql
DELETE FROM tasks
WHERE ctid IN (
  SELECT ctid
  FROM tasks
  WHERE finished_at < NOW() - INTERVAL '3 days'
    AND status IN ('succeeded', 'failed', 'uncertain')
  ORDER BY finished_at ASC
  LIMIT 1000
  FOR UPDATE SKIP LOCKED
);
```

`task_payloads` 建议清理口径：

- 非终态且尚未真正发出的任务：保留 payload
- 已 `request_dispatched_at` 的任务：可在短暂缓冲后清理 payload
- 已终态的任务：建议 `10m ~ 60m` 内清理 payload

建议 SQL 形态：

```sql
DELETE FROM task_payloads
WHERE ctid IN (
  SELECT p.ctid
  FROM task_payloads p
  JOIN tasks t ON t.task_id = p.task_id
  WHERE
    p.payload_expires_at < NOW()
    AND (
      t.finished_at IS NOT NULL
      OR t.request_dispatched_at IS NOT NULL
    )
  ORDER BY p.payload_expires_at ASC
  LIMIT 1000
  FOR UPDATE SKIP LOCKED
);
```

二阶段增强：

- 若任务量进一步升高到按天分表更划算
- 可再评估按天分区或归档表
- 但首版不建议一开始就引入分区复杂度

### 7.6 高并发优化优先级

优先顺序建议：

1. SQL 和索引先对
2. `Retry-After` + 限流先立住
3. 增加内存热点缓存
4. 控制连接池
5. 限制 worker 并发
6. TTL 清理做小批量化
7. 必要时再引入 Redis

### 7.7 核心监控指标

首版至少应覆盖以下 metrics：

- 提交接口 QPS / 错误率
- 查询接口 QPS / `429` 比例
- 队列深度 / 入队失败数
- worker 忙碌数 / 利用率
- 任务终态分布：`succeeded / failed / uncertain`
- 任务总耗时 `p50 / p95 / p99`
- `NewAPI` 请求耗时与状态码分布
- PostgreSQL 连接池使用率
- 恢复扫描重入队数 / 标记 `uncertain` 数

---

## 八、非功能需求

| 类别 | 要求 |
|---|---|
| 提交接口响应 | `p99 <= 300ms ~ 500ms` |
| 单任务总超时 | `12 分钟` |
| Go → NewAPI 超时 | `11 分钟` 左右 |
| 轮询退避 | `accepted / queued / running` 返回 `Retry-After: 3 ~ 5` |
| 入口硬上限 | 解压后请求体 `<= 2MB` |
| 状态一致性 | PostgreSQL 为准 |
| 任务摘要保留期 | 最近 `3 天` 完整保留 |
| 恢复 payload 保留期 | 仅短期保留，不作为用户查询数据 |
| 查询归属校验 | 必须校验当前 `Authorization` 所属用户 |
| Key 安全 | 用户 API Key 不得明文落日志/接口 |
| 秘钥管理 | `OWNER_HASH_SECRET` 与 payload 加密密钥必须稳定持久化 |
| 超时梯度 | 入口层 > Gateway > NewAPI > 上游实际超时 |
| 优雅停机 | 先 drain，最后再条件标记 `uncertain` |
| 向后兼容 | `NewAPI` 同步接口继续保留 |
| 高并发目标 | 允许高并发轮询与中高并发任务写入 |

---

## 九、与 v4.0 / A2-CF 的对比结论

### 相比 v4.0

优势：

- 客户端不再依赖 `NewAPI` 长连接链路
- 不借壳 Sora 协议
- 状态源更自然

代价：

- 新增一层 Go 异步网关服务
- 客户端需要改造轮询接口

### 相比 A2-CF

优势：

- 少一段公网长链路
- 不依赖 `CF Workflows / DO / R2`
- 安全边界更可控
- 调试和排障更直接
- 后续网页即使部署在 `CF`，后端查询仍只需要打 Go 网关

代价：

- 自己承担任务执行器和状态服务
- 需要自己管 worker、数据库、限流

### 综合判断

如果你的前提是：

- `Go` 网关和 `NewAPI` 在同机或同内网
- 你想从一开始就考虑高并发
- 你已有 PostgreSQL 服务实例可复用

那么 **A3-LocalGateway-HC 很可能是三案里最值得优先实现的主线方案**。

---

## 十、开发里程碑

| 阶段 | 任务 | 产出 |
|---|---|---|
| D1 | Go 网关骨架；部署拓扑定稿；提交接口；PostgreSQL 建库建表；`owner_hash` 策略定稿 | 提交端点可用 |
| D2 | 本地 worker pool；任务队列；状态机流转 | 任务可异步执行 |
| D3 | 同机转发 NewAPI；HTTP 状态码分类；成功/失败写回摘要 | 端到端闭环 |
| D4 | 查询状态接口、最近任务列表、cursor 分页、content 端点；归属校验 | 客户端轮询与网页查询可用 |
| D5 | 重启恢复扫描、`uncertain` 终态、心跳机制、优雅停机、payload 生命周期 | 异常恢复语义可用 |
| D6 | 连接池、索引、轮询退避/限流、TTL 小批量清理、内存缓存、metrics | 高并发优化 |
| D7 | 弱网与异常链路验证；小流量灰度 | 上线验证 |

---

## 十一、最终建议

若你现在的真实约束是：

- `NewAPI` 与新中间层就在同一台机器
- 已经有 PostgreSQL 服务实例
- 你希望从第一版就按高并发思路来

那么我建议：

- **不再把 SQLite 作为主线**
- **第三方案直接按 PostgreSQL 版 A3 来设计**
- **PostgreSQL 服务实例可以共用，但数据库边界必须独立**
- **最近 `3 天` 长保留的是任务摘要与结果摘要，不是完整请求 payload**
- **后续网页若上线，也仍通过 Go 网关查询，不直连数据库**
- **首版接受“历史记录与 API Key 绑定”的产品取舍**
- **首版必须补齐轮询退避、优雅停机、超时梯度、TTL 小批量清理**
- **完整请求 payload 只为恢复重放而短期保存，不进入最近 `3 天` 查询主数据面**

一句话总结：

**同机 Go Async Gateway + 独立数据库边界的 PostgreSQL + 最近 `3 天` 任务摘要保留 + 短期恢复 payload + 内网长连接到 NewAPI，通常会比 A2-CF 更稳、更简单，也更适合作为高并发主线。**
