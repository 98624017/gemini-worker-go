# 香蕉Pro 图像生成异步化 A2-DO 方案设计文档

**客户端直提 CF · DO 权威状态源 · Workflow 长等待执行 · NewAPI 保留同步计费**

涉及系统：客户端 · CF Worker · CF Durable Object · CF Workflows · CF R2 · NewAPI · Gemini Worker 上游

---

## 一、背景与目标

### 1.1 现有链路痛点

当前同步链路的核心问题不是“生图慢”本身，而是“慢请求由最弱的一段链路承担”。

- **客户端链路过弱**
  - 移动端息屏
  - 网络切换
  - VPN 抖动
  - 劣质运营商链路
  - 网页前台标签页挂起
- **同步等待时间过长**
  - 生图常见耗时 30 ～ 420 秒
  - 你的 `NewAPI` 前置 `Nginx` 已设置到 `600s`
  - 实际上多数请求能成功，但总会有一部分用户网络先掉
- **一旦客户端先断，成功结果难回收**
  - 用户已发出请求
  - 上游可能仍在继续生图
  - 最终图可能已经成功生成
  - 但客户端因长连接断开拿不到结果
- **计费逻辑不宜搬走**
  - `NewAPI` 现有同步计费逻辑已经验证过
  - 预扣 / 成功扣 / 失败退这条链路不应轻易外迁

### 1.2 改造思路

本方案不再让客户端先连 `NewAPI`，而是将“异步任务入口”前移到 `CF`：

```text
客户端 ──短连接提交──▶ CF
客户端 ◀─立即返回 task_id── CF

CF Workflow ──长连接直连源站──▶ NewAPI
CF Workflow ◀──── 同步等待最终结果 ──── NewAPI

客户端 ──短轮询──▶ CF
客户端 ◀─状态/结果── CF
```

关键点：

- 客户端只做短连接提交和短轮询
- `CF` 代用户持有并透传其 `NewAPI API Key`
- `CF Workflow` 直连你的服务器，不经过 Cloudflare CDN 代理层
- `NewAPI` 继续按原同步逻辑完成鉴权、预扣、成功扣、失败退

### 1.3 本方案核心价值

- **客户端弱网问题被显著削弱**
  - 中间断网不会导致整单直接丢失
  - 恢复后继续轮询同一个 `task_id` 即可
- **不再借壳 Sora 协议**
  - 不需要再把图片任务伪装成视频任务
  - 协议语义更直接
- **状态真相源可做强一致**
  - 使用 `Durable Object`
  - 不再依赖 `KV` 最终一致兜底
- **计费不迁移**
  - `CF` 不负责余额和计费
  - `NewAPI` 继续保持计费权威地位

### 1.4 目标与非目标

**目标：**

- 客户端提交生图请求后，`500ms` 级返回任务受理结果
- 客户端可通过 `task_id` 持续轮询最终图像结果
- 最终图片结果使用当前项目既有 `output=url` 语义
- 整体协议尽量贴近当前 Gemini 风格下游请求体
- 为后续网页端接入保留兼容空间

**非目标：**

- 本期不要求 `NewAPI` 提供异步任务接口
- 本期不要求 `NewAPI` 提供回调接口
- 本期不实现任务取消
- 本期不实现任务列表、历史搜索、推送通知
- 本期不实现“绝对零不确定态”；只做显著降低，而非从理论上彻底消灭

---

## 二、整体架构

### 2.1 三种链路对比

**原始同步链路：**

```text
客户端 ──同步HTTP──▶ NewAPI ──同步HTTP──▶ 上游
客户端 ◀────────────── 长连接等待 30~420s ──────────────
```

**v4.0 链路：**

```text
客户端 ──▶ NewAPI（Sora渠道）
NewAPI ──▶ CF Worker
CF Worker ──▶ CF Workflow ──▶ 上游
NewAPI ◀── 轮询 CF 状态
客户端 ◀── NewAPI 最终返回
```

**A2-DO 链路：**

```text
客户端 ──▶ CF Worker（异步任务入口）
CF Worker ──▶ DO（创建任务）
CF Worker ──▶ Workflow（异步执行）
Workflow ──▶ 源站 NewAPI（同步等待）
Workflow ──▶ DO（写终态）
客户端 ◀── 轮询 CF 获取结果
```

### 2.2 职责边界

| 层级 | 组件 | 职责 | 是否改动 |
|---|---|---|---|
| 客户端 | App / 脚本 / 后续网页 | 提交任务、轮询状态、获取最终结果 | 需要改造 |
| 协议入口 | CF Worker | 接收任务、快速返回、校验、查询状态 | 新开发 |
| 权威状态源 | Durable Object | 保存任务状态、执行串行化、归属校验 | 新开发 |
| 大对象存储 | CF R2 | 存放大请求体、加密密钥对象、调试快照 | 新配置 |
| 异步执行器 | CF Workflows | 读取任务输入、长等待调用 NewAPI、回写终态 | 新开发 |
| 计费/鉴权 | NewAPI | 校验用户 Key、同步生图、预扣/成功扣/失败退 | 逻辑保留 |
| 图像生成 | 现有 Gemini Worker / 上游渠道 | 真实生成图片并返回结果 | 零改动或最小改动 |

### 2.3 为什么是 DO + Workflow + R2

**Durable Object：**

- 每个任务是天然的单状态机
- 状态迁移必须串行
- 任务是否存在、是否成功、是否失败不能受最终一致影响
- 任务查询接口需要强归属校验

**Workflow：**

- 适合承接长时间等待
- 能把“接收请求”和“等待结果”解耦
- 对客户端立即返回，不占住客户端连接

**R2：**

- 请求体虽然去掉了大 base64 图片，但仍可能因长 URL、扩展字段而变大
- 不应让 Workflow payload 贴着 `1MiB` 上限跑
- 需要短期保存加密后的敏感对象或大请求体

### 2.4 为什么不用 KV 做真相源

本方案中，`KV` 最多是缓存，不是权威源。

原因：

- `KV` 是最终一致
- 边缘节点间同步存在延迟
- 任务查询若返回错误的 `404` 或旧状态，会直接影响用户判断
- 本方案又是和计费结果强相关的任务查询，不适合承受最终一致误判

---

## 三、客户端异步任务协议规范

本方案刻意让**提交请求体尽量贴近当前项目的 Gemini 风格下游协议**，避免重新发明一套 `images[]` 私有协议。

### 3.1 提交任务

```http
POST /v1beta/models/{model}:generateContent
Authorization: Bearer <newapi-user-api-key>
Content-Type: application/json
Content-Encoding: gzip | br | identity
```

#### 设计原则

- 路径继续使用当前项目的 `generateContent` 风格
- `model` 继续走路径参数
- 请求体继续使用 Gemini 风格 JSON
- 参考图继续走 `inlineData.data=http(s)://...`
- 这套 `CF` 入口天然只服务异步任务，不再需要 `?async=1`
- `output=url` 固定要求最终结果返回 URL，而非 base64

#### 客户端提交示例

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
        },
        {
          "inlineData": {
            "mimeType": "image/png",
            "data": "https://example.com/input-b.png?sign=xxx"
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
  "id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "status": "accepted",
  "polling_url": "/v1/tasks/img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "content_url": "/v1/tasks/img_01JQ2QJQ7Y1YQ2ABCDEF123456/content"
}
```

> 受理成功只表示 `CF` 已接住任务并成功启动异步链路，不表示 `NewAPI` 已实际完成生图。

#### 字段约束

| 字段 | 位置 | 必填 | 说明 |
|---|---|---|---|
| `output` | Query / Body / `generationConfig.imageConfig.output` | 是 | 最终解析结果必须为 `url` |
| `Authorization` | Header | 是 | 用户自己的 `NewAPI API Key` |
| `{model}` | Path | 是 | 继续沿用当前 Gemini 路径风格 |
| `contents` | Body | 是 | Gemini 风格请求体 |
| `inlineData.data` | Body | 否 | 允许 `http/https` URL；不建议大 base64 |

### 3.2 轮询任务状态

```http
GET /v1/tasks/{task_id}
Authorization: Bearer <newapi-user-api-key>
```

#### 执行中

```json
{
  "id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "status": "running",
  "progress": 50
}
```

#### 成功

```json
{
  "id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "finished_at": 1773964898,
  "status": "succeeded",
  "progress": 100,
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
  "id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
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

#### 不确定失败

用于表达“`CF -> NewAPI` 连接中断时，无法确认 `NewAPI` 是否已完成并计费”的特殊场景：

```json
{
  "id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "finished_at": 1773964850,
  "status": "failed",
  "error": {
    "code": "upstream_transport_uncertain",
    "message": "connection to newapi broke after request dispatch; task result may be uncertain"
  }
}
```

> 这是 A2-DO 相对 v4.0 的一个重要代价：`CF` 与 `NewAPI` 的单次同步调用，仍然存在低概率“结果不确定态”。本方案的目标是显著降低，而不是在没有 `NewAPI` 异步接口的前提下从理论上彻底消灭。

### 3.3 获取图片内容（可选）

```http
GET /v1/tasks/{task_id}/content
Authorization: Bearer <newapi-user-api-key>
```

行为：

- 成功：`302 Location: https://...`
- 未完成：`409`
- 失败：`409`
- 不存在：`404`
- 非任务归属用户：`403`

## 四、计费设计

### 4.1 计费所有权不迁移

本方案最重要的约束之一：

- `CF` 不维护余额
- `CF` 不做扣费公式计算
- `CF` 不持有用户账务状态
- `CF` 不做退费补偿逻辑

计费仍由 `NewAPI` 完成。

### 4.2 计费全链路

| 阶段 | 执行方 | 说明 |
|---|---|---|
| 用户发起请求 | 客户端 | 携带自己的 `NewAPI API Key` 调 `CF` |
| 任务受理 | CF | 仅创建任务，不计费 |
| 同步生图调用 | Workflow → NewAPI | 透传用户 `API Key` 发起真实请求 |
| 预扣 / 成功扣 / 失败退 | NewAPI | 继续走当前已验证链路 |
| 状态回传 | CF | 只把结果状态包装为任务接口返回 |

### 4.3 A2-DO 的计费风险边界

本方案相较 v4.0 有一个新增风险：

- `Workflow` 与 `NewAPI` 的同步连接如果在请求发出后中断
- `CF` 可能不知道 `NewAPI` 最终是否成功
- 若盲目重试，可能会造成重复生成或重复扣费

因此，必须明确：

1. **Step 2 不允许无脑自动重试**
2. **只有在确定请求未发出到 `NewAPI` 前，才允许安全重试**
3. **一旦请求已发出且结果不明，必须写不确定失败码**

该风险无法在“不新增 `NewAPI` 异步查询接口”的前提下被完全消除，只能通过：

- `CF` 直连源站
- 稳定的机房网络
- 明确的超时与重试边界
- `X-Banana-Task-ID` 追踪

来降低概率与排障成本。

### 4.4 为什么仍然值得做

即使存在上述低概率不确定态，本方案仍能显著改善主问题：

- 把“最脆弱的客户端长连接”换成“更稳定的 `CF -> 源站` 长连接”
- 大幅降低丢图率
- 保留 `NewAPI` 已验证的计费语义

---

## 五、CF 详细功能需求

### 5.1 端点实现清单

| 端点 | 方法 | 说明 |
|---|---|---|
| `/v1beta/models/{model}:generateContent  &output=url` | `POST` | 提交异步图像任务 |
| `/v1/tasks/{id}` | `GET` | 查询任务状态 |
| `/v1/tasks/{id}/content` | `GET` | 重定向到最终图片 URL |

### 5.2 提交端点处理流程

```text
1. 校验 Authorization，缺失返回 401
2. 校验 `output` 最终解析为 `url`，否则返回 400
3. 读取并解压请求体（gzip / br / identity）
4. 解析 JSON，按 Gemini 风格做格式校验
5. 计算解压后 body 大小
6. 生成 task_id = "img_" + ULID/UUID
7. 计算 owner_hash（由 API Key 派生，不保存明文）
8. 加密保存用户 API Key
9. 按 body 大小决定：直传 Workflow / 先写 R2
10. 写 DO 初始状态 accepted
11. 启动 Workflow
12. 返回 202 Accepted + task object
```

#### 关键要求

- 提交接口不能等待生图完成
- 提交接口不能在主路径中抓取参考图 URL
- 提交接口不能在主路径中调用 `NewAPI`
- 任何提交流程失败，都不能留下“客户端已拿到 task_id 但 DO 中无记录”的悬挂任务

#### 每次提交均创建新任务

和 v4.0 一样，本方案不做 prompt 级去重：

- 相同请求体多次提交，视为多次独立抽卡
- 不做内容幂等缓存
- 不做结果重放

### 5.3 查询端点处理流程

```text
1. 校验 Authorization
2. 从 API Key 派生 owner_hash
3. 根据 task_id 路由到 DO
4. 读取任务状态
5. 若任务不存在 → 404
6. 若 owner_hash 不匹配 → 403
7. 返回 DO 当前状态
```

#### 查询端点原则

- 只认 DO，不依赖 KV
- 只要 DO 中有任务，就绝不能返回伪 `404`
- 任务归属必须校验，禁止“知道 task_id 就能查别人的图”

### 5.4 content 端点处理流程

```text
1. 校验 Authorization
2. 校验任务归属
3. 读取 DO 终态结果
4. 若未完成 → 409
5. 成功时从 response 中提取第一张图片 URL
6. 返回 302 跳转
```

### 5.5 Durable Object 状态模型

建议字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| `task_id` | string | 全局任务 ID |
| `status` | string | `accepted / running / succeeded / failed` |
| `created_at` | int64 | 创建时间 |
| `updated_at` | int64 | 最近更新时间 |
| `finished_at` | int64 | 完成时间 |
| `model` | string | 请求路径中的模型名 |
| `owner_hash` | string | 从用户 API Key 派生的归属标识 |
| `workflow_instance_id` | string | Workflow 实例 ID |
| `request_storage_mode` | string | `inline` 或 `r2_ref` |
| `request_inline` | object | 小请求体的精简保存，可选 |
| `request_ref` | string | 大请求体的 R2 引用 |
| `encrypted_api_key_ref` | string | 加密密钥对象引用，或 DO 内加密字段 |
| `response_snapshot` | object | 成功时保存的下游响应体 |
| `error_code` | string | 失败码 |
| `error_message` | string | 失败信息 |
| `transport_uncertain` | bool | 是否属于“不确定失败” |

### 5.6 R2 存储策略

R2 主要承担 3 类对象：

#### 1. 大请求体

当解压后的 JSON 请求体 `> 800KB`：

- 原始请求体写入 `R2`
- DO 中只存 `request_ref`
- Workflow 启动后按引用回读

#### 2. 加密密钥对象

如不想在 DO 中保存密文字段，可将加密后的用户 API Key 落 R2：

- 提交阶段加密
- Workflow 阶段解密使用
- 终态写回后按 TTL 清理

#### 3. 可选调试快照

仅在确有需要时保留：

- 提交请求快照
- 下游响应快照
- 仅用于排障
- 默认不建议长期保留

### 5.7 Workflow 执行步骤与超时预算

**总超时建议：12 分钟**

原因：

- `NewAPI` 前置 `Nginx` 当前设为 `600s`
- 需要额外预留：
  - 读取输入
  - 状态写回
  - 清理动作

#### Step 0：启动与状态切换

| 项 | 说明 |
|---|---|
| 目标 | 将任务从 `accepted` 标记为 `running` |
| 超时 | `15s` |
| 行为 | 写 `workflow_instance_id`、更新时间 |
| 失败处理 | 重试 2 次；仍失败则写 `failed/workflow_state_error` |

#### Step 1：读取输入与解密密钥

| 项 | 说明 |
|---|---|
| 目标 | 读取请求体、解密用户 API Key |
| 超时 | `30s` |
| 行为 | 回读 `R2` 或使用 inline payload |
| 失败处理 | 写 `failed/input_load_failed` |

#### Step 2：直连 NewAPI 同步等待

| 项 | 说明 |
|---|---|
| 目标 | 使用用户 API Key 发起真实同步生图请求 |
| 超时 | `11 min` |
| 行为 | 复用当前同步生图接口，等待最终返回 |
| 失败处理 | 见 §5.8 |

#### Step 3：结果标准化与写终态

| 项 | 说明 |
|---|---|
| 目标 | 存储成功结果或失败原因 |
| 超时 | `30s` |
| 行为 | 保存 `response_snapshot` / `error_*` |
| 失败处理 | 重试 3 次，仍失败标记 `workflow_persist_failed` |

#### Step 4：清理临时敏感数据

| 项 | 说明 |
|---|---|
| 目标 | 清理可删的密钥对象和临时引用 |
| 超时 | `30s` |
| 行为 | 删除或标记加密对象待清理 |
| 失败处理 | 记录日志，不影响终态返回 |

### 5.8 Step 2 的重试与不确定态策略

这是 A2-DO 最关键的细节。

#### 安全重试原则

只有在**能够确认请求尚未成功发出到 `NewAPI`** 时，才允许重试。

#### 决策表

| 场景 | 是否重试 | 原因 |
|---|---|---|
| DNS 解析失败 / TCP 建连失败 | 可重试 1 次 | 请求大概率尚未送达应用层 |
| TLS 握手失败 | 可重试 1 次 | 请求大概率尚未送达应用层 |
| HTTP 请求已建立，读取响应前连接断开 | 禁止自动重试 | 无法确认 `NewAPI` 是否已开始处理 |
| 上游读超时（接近 600s） | 禁止自动重试 | 可能已在 `NewAPI` 内执行中或已完成 |
| `NewAPI` 明确返回 4xx / 5xx JSON | 不重试 | 直接以返回结果判定 |

#### 不确定态处理

当发生“请求可能已送达，但结果无法确认”的网络错误时：

1. DO 终态写 `failed`
2. `error.code = upstream_transport_uncertain`
3. `transport_uncertain = true`
4. 日志中必须带上：
   - `task_id`
   - `workflow_instance_id`
   - `newapi_request_id`（如果可取）
   - `X-Banana-Task-ID`

### 5.9 直连源站的连接策略

本方案明确要求 `CF -> NewAPI` 为**直连源站**，不经过 Cloudflare 代理层。

建议：

- `NEWAPI_BASE_URL` 指向源站地址或专用内网/灰云地址
- 源站 `Nginx` 继续保持 `600s` 级超时
- `CF` 侧请求超时应略大于源站读超时
- 透传 `X-Banana-Task-ID`
- 避免中间再套一层可能 100~120 秒超时的代理

---

## 六、NewAPI 对接规范

### 6.1 对接方式

Workflow 直接调用 `NewAPI` 当前同步生图接口。

#### 示例

```http
POST {NEWAPI_BASE_URL}{原始请求路径与查询串}
Authorization: Bearer <user-api-key>
Content-Type: application/json
```

说明：

- 原始请求路径保持不变
- 原始 query 保持不变
- 原始请求体保持不变
- **只替换 baseUrl**

### 6.2 透传与新增 Header

#### 必传

- `Authorization: Bearer <user-api-key>`
- `Content-Type: application/json`

#### 建议新增

- `X-Banana-Task-ID: <task_id>`
- `X-Banana-Async-Source: cf-workflow`
- `X-Request-ID: <uuid>`

这些 Header 不参与业务判定，只用于排障与日志关联。

### 6.3 请求体透传规则

客户端到 `CF` 的请求在通过入口校验后，转发到 `NewAPI` 时应保持原样。

**保持不变：**

- 原始 path
- 原始 query
- `contents`
- `generationConfig`
- `generation_config`
- 参考图 URL 型 `inlineData.data`

**唯一变化：**

- `baseUrl` 从 `CF` 自身地址替换为 `NEWAPI_BASE_URL`
- 追加排障 Header，如 `X-Banana-Task-ID`

**入口校验要求：**

- 最终 `output` 语义必须为 `url`
- 但一旦校验通过，转发到 `NewAPI` 时不再改写 body 或 query

### 6.4 NewAPI 返回结果预期

成功时，期望 `NewAPI` 返回当前项目既有的 `output=url` 风格结果，即：

- 响应体仍为 Gemini 风格 JSON
- 图片 part 的 `inlineData.data` 为图片 URL
- 可为 `/proxy/image?...`
- 也可为已代理包装后的公网 URL

失败时，`NewAPI` 返回的非 `200` JSON 错误体将被 `CF` 记录并映射为任务失败。

### 6.5 NewAPI 返回结果在 CF 的保存策略

本方案里，`CF` 不做新的图片托管层。

也就是说：

- `CF` 不再二次上传图床
- `CF` 不再维护 `uguu / R2` 双图床决策
- `CF` 只存 `NewAPI` 的最终响应快照

`content` 端点只是从成功结果中提取第一个图片 URL 并 `302` 跳转。

### 6.6 为什么本方案不要求 NewAPI 新增异步接口

原因很明确：

- 一期目标是先解决客户端长连接问题
- 不希望同时大改 `NewAPI`
- 只要 `CF -> NewAPI` 的源站链路更稳，客户端体验已经能大幅改善

但也必须记录：

- 如果未来 `NewAPI` 能提供任务查询或回调接口
- A2-DO 的不确定态问题可以进一步收敛

---

## 七、协议转换与结果标准化

### 7.1 客户端入站协议

客户端提交到 `CF` 的请求协议，继续沿用当前项目的 Gemini 兼容风格：

- 路径：`/v1beta/models/{model}:generateContent`
- body：`contents + generationConfig`
- 参考图：`inlineData.data=http(s)://...`
- 输出模式：`output=url`

### 7.2 `output=url` 兼容规则

与当前项目一致，`CF` 应按以下优先级解析输出模式：

1. query: `?output=url`
2. body 顶层：`"output": "url"`
3. `generationConfig.imageConfig.output`
4. `generation_config.image_config.output`

最终解析结果若不是 `url`，异步接口直接拒绝。

### 7.3 参考图输入规则

本方案里，参考图推荐走 URL，而不是大 base64。

支持形式：

```json
{
  "inlineData": {
    "mimeType": "image/png",
    "data": "https://example.com/a.png"
  }
}
```

不推荐形式：

```json
{
  "inlineData": {
    "mimeType": "image/png",
    "data": "iVBORw0K..."
  }
}
```

原因：

- 大 base64 会直接放大任务入口请求体
- 违背本方案“轻入口、重执行”的目标
- 容易迫使请求体频繁走 `R2` 外置

### 7.4 成功结果标准化

任务成功时，`GET /v1/tasks/{id}` 中的 `response` 字段应尽量保持与当前同步接口返回一致。

推荐只做以下最小包装：

- 外层新增任务元字段
- 内层 `response` 保持原有下游响应结构

这样客户端只需要在“任务完成后”继续沿用已有 Gemini 响应解析逻辑。

### 7.5 content 提取规则

`GET /v1/tasks/{id}/content` 的 URL 提取策略：

1. 从 `response.candidates[0].content.parts` 中找第一个图片 part
2. 读取其 `inlineData.data`
3. 若值为 URL，则 `302` 跳转
4. 若无图片 URL，则返回 `409` 或 `500`

### 7.6 错误结果标准化

异步任务接口不直接透传 `NewAPI` 的 HTTP 状态码给轮询客户端，而是包装成任务状态：

| `NewAPI` 结果 | 任务状态 |
|---|---|
| `200` 成功返回 | `succeeded` |
| `4xx / 5xx` 明确返回 | `failed` |
| 网络错误且结果不明 | `failed + upstream_transport_uncertain` |

---

## 八、生产级处理细则

### 8.1 入参校验

所有校验必须在触发 Workflow 前完成，校验失败直接返回，不消耗 Workflow 配额。

| 项目 | 规则 | 不合规处理 |
|---|---|---|
| `Authorization` | 必填 | `401` |
| `async` | 必须为开启值 | `400` |
| `output` | 最终解析结果必须为 `url` | `400` |
| `{model}` | 必填，非空 | `400` |
| 请求体 | 必须为合法 JSON | `400` |
| `contents` | 必须存在 | `400` |
| prompt 文本 | 建议 1 ～ 4000 字 | `400` |
| 参考图数量 | 最多 `8` 张 | `400` |
| 单 URL 长度 | 建议 `<= 4096` 字符 | `400` |
| 参考图协议 | 仅允许 `http / https` | `400` |
| 解压后请求体大小 | 建议硬上限 `2MB` | `413` |

> 这里的 `2MB` 是入口硬限制，不是 Workflow 直传阈值。  
> Workflow 直传阈值为 `800KB`，超过后可进入 `R2` 外置模式；超过入口硬上限则直接拒绝。

### 8.2 压缩与大小分流策略

#### 客户端到 CF

- 默认：`gzip`
- 可选：`br`
- 兼容：`identity`

#### CF 内部判定

以**解压后的 JSON UTF-8 字节数**为准：

| 条件 | 处理方式 |
|---|---|
| `<= 800KB` | 直接进入 Workflow payload |
| `> 800KB && <= 2MB` | 请求体写入 `R2`，Workflow 只传 pointer |
| `> 2MB` | 拒绝请求，返回 `413` |

### 8.3 DO 一致性边界

`DO` 是唯一权威状态源。

缓解规则：

1. 所有状态写入都经过同一个任务对应的 DO 实例
2. 终态只写一次
3. 轮询接口仅读 DO
4. 即使未来加入 KV 缓存，KV 也只能做加速层

### 8.4 任务归属校验

必须防止“知道 task_id 即可查图”。

建议：

- 使用用户 `API Key` 做 `HMAC-SHA256` 派生 `owner_hash`
- DO 仅保存 `owner_hash`
- 轮询时根据当前 `Authorization` 再算一次
- 不保存明文 key 作为归属字段

### 8.5 敏感信息保护

用户 API Key 处理原则：

- 不写明文日志
- 不回显给客户端
- 不写入任务结果接口
- 提交后立即加密
- 只在 Workflow 执行时短暂解密
- 到期清理

### 8.6 结构化日志规范

```json
{
  "ts": "2026-03-19T10:00:00Z",
  "task_id": "img_01JQ2QJQ7Y1YQ2ABCDEF123456",
  "workflow_instance_id": "wf_123",
  "event": "task_accepted | workflow_started | newapi_call_started | newapi_call_succeeded | newapi_call_failed | task_succeeded | task_failed",
  "model": "gemini-3-pro-image-preview",
  "request_storage_mode": "inline | r2_ref",
  "payload_bytes": 182340,
  "duration_ms": 1240,
  "transport_uncertain": false,
  "error_code": "",
  "error_msg": ""
}
```

### 8.7 错误码规范

| `error.code` | 触发场景 | 客户端处理建议 |
|---|---|---|
| `invalid_request` | 请求参数不合法 | 修正参数后重试 |
| `missing_api_key` | 未携带用户 API Key | 重新鉴权 |
| `request_too_large` | 解压后请求体超过硬上限 | 减少图片 URL / 缩短参数 |
| `workflow_launch_failed` | Workflow 启动失败 | 稍后重试 |
| `input_load_failed` | R2/解密/输入读取失败 | 稍后重试 |
| `auth_failed` | `NewAPI` 鉴权失败 | 检查 API Key |
| `upstream_error` | `NewAPI` 明确返回业务错误 | 按消息提示处理 |
| `upstream_timeout` | `NewAPI` 明确超时 | 稍后重试 |
| `upstream_transport_uncertain` | 发出请求后连接异常中断 | 提示结果可能不确定 |
| `workflow_state_error` | 状态写入失败 | 联系支持 |
| `workflow_persist_failed` | 终态持久化失败 | 联系支持 |
| `unknown_error` | 未分类异常 | 联系支持 |

### 8.8 R2 Key 命名规范

#### 请求体对象

```text
requests/{YYYY}/{MM}/{DD}/{task_id}.json
```

示例：

```text
requests/2026/03/19/img_01JQ2QJQ7Y1YQ2ABCDEF123456.json
```

#### 密钥对象

```text
secrets/{YYYY}/{MM}/{DD}/{task_id}.bin
```

#### 调试快照

```text
snapshots/{YYYY}/{MM}/{DD}/{task_id}-response.json
```

### 8.9 TTL 与清理策略

| 对象 | TTL 建议 |
|---|---|
| DO 任务元数据 | `7 天` |
| 大请求体对象 | `24 小时` |
| 加密 API Key 对象 | `2 小时` |
| 成功结果快照 | `7 天` |
| 失败结果快照 | `7 天` |

### 8.10 限流与滥用防护

本方案会让 `CF` 成为真正的任务入口，因此必须补上基础防护：

- 基于 IP / 用户 key 的提交频控
- 同用户并发任务数限制
- 解压后 body 大小限制
- 禁止把 `CF` 当开放代理
- 对明显恶意的超长 URL 或垃圾参数直接拒绝

---

## 九、非功能需求

| 类别 | 要求 |
|---|---|
| 提交接口响应 | `POST ...:generateContent`，`p99 <= 500ms` |
| 任务总超时 | `12 分钟` |
| Workflow 直连 `NewAPI` 超时 | `11 分钟` 左右 |
| Workflow 直传阈值 | 解压后请求体 `<= 800KB` |
| 入口硬上限 | 解压后请求体 `<= 2MB` |
| 任务查询一致性 | 以 `DO` 为准，不依赖 `KV` |
| 归属校验 | 任务查询必须校验当前 `Authorization` 所属用户 |
| Key 安全 | 用户 API Key 不得明文落日志、接口、持久状态 |
| 向后兼容 | `CF` 入口天然异步；`NewAPI` 现有同步接口继续保留 |
| 可观测性 | 必须能按 `task_id` 追踪 `CF` 与 `NewAPI` 两侧日志 |

---

## 十、与 v4.0 方案对比

### 10.1 核心差异表

| 维度 | v4.0 | A2-DO |
|---|---|---|
| 首次提交入口 | `NewAPI` | `CF` |
| 后续轮询发起方 | `NewAPI` | 客户端 |
| 客户端协议形态 | 借壳 Sora 视频协议 | 保持 Gemini 风格提交体 + 自定义任务查询 |
| `CF` 对外角色 | Sora 风格上游 | 真正的异步任务入口 |
| 任务真相源 | 文档中主要依赖 `KV` + Workflow 兜底 | `DO` 强一致状态源 |
| `CF` 是否接触用户 API Key | 否 | 是，短期持有 |
| 计费执行者 | `NewAPI` | `NewAPI` |
| 客户端改造成本 | 中等 | 中等偏高 |
| 协议自然度 | 一般 | 更自然 |

### 10.2 v4.0 的优势

1. `CF` 不需要持有用户 API Key  
2. 更容易复用 `NewAPI` 的异步轮询现成逻辑  
3. 客户端可能只需切换目标接口，而不必理解新的任务协议  

### 10.3 A2-DO 的优势

1. 直接切掉客户端长连接问题  
2. 不再借壳 Sora 语义  
3. 任务状态由 `DO` 强一致管理，更贴近业务本质  
4. 对未来扩展更自然：
   - 任务取消
   - 回调
   - SSE / WebSocket
   - 历史任务列表

### 10.4 A2-DO 的代价

1. `CF` 短期持有用户 API Key，安全边界更重  
2. 客户端必须适配任务查询接口  
3. `CF -> NewAPI` 的长连接若在请求已发出后中断，会出现低概率不确定态  

### 10.5 何时更适合选 v4.0

若你更在意：

- 尽量不让 `CF` 接触用户 API Key
- 尽量不重新定义客户端轮询接口
- 更快复用 `NewAPI` 现有异步轮询语义

则 v4.0 更稳妥。

### 10.6 何时更适合选 A2-DO

若你更在意：

- 把弱网问题直接在客户端入口层解决
- 用更自然的图片任务模型替代 Sora 借壳协议
- 用强一致状态源做后续演进基础

则 A2-DO 更合理。

---

## 十一、开发里程碑

| 阶段 | 任务 | 产出 |
|---|---|---|
| D1 | CF Worker 任务入口骨架；Gemini 风格入口校验；DO 初始写入 | 提交端点可用 |
| D2 | `GET /v1/tasks/{id}`；任务归属校验；基础状态轮询 | 可轮询任务状态 |
| D3 | R2 大请求体外置；`800KB` 分流；加密 API Key 保存 | 输入存储链路可用 |
| D4 | Workflow 启动；直连 `NewAPI`；成功/失败写回 DO | 端到端闭环 |
| D5 | `content` 端点；响应标准化；结构化日志 | 查询与跳转闭环 |
| D6 | 超时与不确定态策略；压测与弱网验证 | 健壮性验证 |
| D7 | 小流量灰度；观察 `transport_uncertain` 比例；全量切换 | 上线 |

---

## 十二、最终建议

如果你现在的目标是拿这份文档去和 `v4.0` 做路线评比，我的结论是：

- **v4.0 更保守**  
  优先级是“让计费和调度继续尽量待在 `NewAPI` 的已知轨道里”。

- **A2-DO 更激进，但更贴近问题本质**  
  它直接把“客户端弱网不适合长连接”这个问题从入口处切掉，同时用 `DO` 替代 `KV` 做任务真相源。

若你愿意接受以下两点：

1. `CF` 在任务执行期内短暂持有用户 `NewAPI API Key`
2. 在没有 `NewAPI` 异步查询接口的前提下，接受极低概率的 `upstream_transport_uncertain`

那么 A2-DO 是一条更自然、也更具长期演进性的方案。
