# 香蕉Pro 图像生成异步化 A3-LocalGateway-HC Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 在当前仓库内新增一个独立的 `async-gateway/` Go 子项目，落地 A3 异步网关最小可用闭环：提交异步任务、持久化状态、后台转发 `NewAPI`、提供状态/列表/取图查询，并具备恢复扫描、优雅停机、TTL 清理与基础抗轮询能力。

**Architecture:** 保持现有根目录 `gemini-worker-go` 服务不被重构，在仓库内新增独立 `async-gateway/` 模块与 Dockerfile。A3 网关只负责异步受理、状态存储、后台执行与查询；`NewAPI` 继续负责鉴权与计费；`gemini-worker-go` 继续负责 URL 型 `inlineData` 转 base64 与现有 `output=url` 逻辑。

**Tech Stack:** Go 1.22、标准库 `net/http` / `httptest` / `context` / `expvar`、PostgreSQL、`github.com/jackc/pgx/v5`、`github.com/golang-migrate/migrate/v4`、`github.com/pashagolub/pgxmock/v4`。

---

## 实施前锁定决策

- 新服务目录固定为 `async-gateway/`，使用**独立 Go module**，不污染现有根模块依赖。
- 首版只实现设计文档中的 4 个业务端点：
  - `POST /v1beta/models/{model}:generateContent`
  - `GET /v1/tasks/{id}`
  - `GET /v1/tasks`
  - `GET /v1/tasks/{id}/content`
- 首版**不实现**：
  - 任务取消
  - 回调通知
  - Redis
  - `task_events`
  - 新的同步入口
- 最近 `3 天` 用户查询只依赖 `tasks` 表中的摘要数据。
- `task_payloads` 仅用于恢复重放，不作为用户查询接口的数据来源。
- A3 worker 不重复实现 `gemini-worker-go` 已有的 URL 拉取、base64 转换、图床上传和图片响应整理逻辑。

## 目标目录布局

```text
async-gateway/
├── cmd/
│   ├── banana-async-gateway/
│   │   └── main.go
│   └── banana-async-migrate/
│       └── main.go
├── internal/
│   ├── app/
│   ├── cache/
│   ├── cleanup/
│   ├── config/
│   ├── domain/
│   ├── httpapi/
│   ├── metrics/
│   ├── queue/
│   ├── ratelimit/
│   ├── recovery/
│   ├── security/
│   ├── store/
│   ├── validation/
│   └── worker/
├── migrations/
├── deploy/
├── Dockerfile
├── .dockerignore
├── go.mod
├── go.sum
└── README.md
```

## Task 1: 搭建独立 async-gateway 骨架

**Files:**
- Create: `async-gateway/go.mod`
- Create: `async-gateway/cmd/banana-async-gateway/main.go`
- Create: `async-gateway/internal/app/app.go`
- Create: `async-gateway/internal/config/config.go`
- Create: `async-gateway/internal/config/config_test.go`
- Create: `async-gateway/internal/httpapi/router.go`
- Create: `async-gateway/Dockerfile`
- Create: `async-gateway/.dockerignore`
- Create: `async-gateway/README.md`

### Step 1: 记录仓库基线

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation
go test ./... -count=1
```

Expected: 根目录现有测试全部 PASS，作为引入新子项目之前的基线。

### Step 2: 初始化独立 Go module 与启动入口

- 在 `async-gateway/go.mod` 初始化独立 module。
- 仅加入首批最小依赖：
  - `pgx/v5`
  - `golang-migrate/v4`
  - `pgxmock/v4`
- 在 `cmd/banana-async-gateway/main.go` 中只做：
  - 读取配置
  - 创建 `app.App`
  - 启动 HTTP Server
  - 响应进程信号

### Step 3: 先写配置解析测试

为 `internal/config/config.go` 写表驱动测试，覆盖：

- 必填项缺失：
  - `NEWAPI_BASE_URL`
  - `POSTGRES_DSN`
  - `OWNER_HASH_SECRET`
  - `TASK_PAYLOAD_ENCRYPTION_KEY`
- 默认值注入：
  - `MAX_INFLIGHT_TASKS`
  - `MAX_QUEUE_SIZE`
  - `TASK_POLL_RETRY_AFTER_SEC`
  - `NEWAPI_REQUEST_TIMEOUT_MS`
  - `SHUTDOWN_GRACE_PERIOD_SEC`
- 无效 duration / integer 自动回退默认值

### Step 4: 实现配置与最小路由骨架

- `internal/config/config.go` 负责环境变量解析、默认值、启动前校验。
- `internal/httpapi/router.go` 先注册目标业务路由，未实现的 handler 可临时返回 `501`。
- `internal/app/app.go` 先把配置、logger、router、server 生命周期组织起来。

### Step 5: 验证可编译与配置测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/config -count=1
go build ./cmd/banana-async-gateway
```

Expected: 配置测试 PASS，主二进制可构建。

### Step 6: Commit

```bash
git add async-gateway
git commit -m "feat: scaffold standalone async gateway service"
```

---

## Task 2: 落域模型、鉴权归属与请求校验

**Files:**
- Create: `async-gateway/internal/domain/task.go`
- Create: `async-gateway/internal/security/owner_hash.go`
- Create: `async-gateway/internal/security/owner_hash_test.go`
- Create: `async-gateway/internal/security/payload_crypto.go`
- Create: `async-gateway/internal/security/payload_crypto_test.go`
- Create: `async-gateway/internal/validation/gemini_request.go`
- Create: `async-gateway/internal/validation/gemini_request_test.go`

### Step 1: 先写失败用例

测试至少覆盖：

- `Authorization` 缺失或格式错误
- `output` 最终解析不为 `url`
- prompt 超过 `4000` 字
- 参考图数量超过 `8`
- 参考图协议不是 `http/https`
- 解压后 body 超过硬上限
- 同一个 API Key 生成稳定 `owner_hash`
- 更换 API Key 后 `owner_hash` 必然变化
- payload 加密后可解密，密钥错误时失败

### Step 2: 实现领域模型与安全辅助函数

- `internal/domain/task.go` 定义：
  - `TaskStatus`
  - `Task`
  - `TaskSummary`
  - `TaskPayload`
  - `ResultSummary`
- `internal/security/owner_hash.go` 实现：
  - Bearer Token 归一化
  - `HMAC-SHA256(OWNER_HASH_SECRET, normalized_api_key)`
- `internal/security/payload_crypto.go` 实现：
  - 用 `TASK_PAYLOAD_ENCRYPTION_KEY` 加密/解密 `Authorization`
  - 明确密钥长度与启动时校验规则

### Step 3: 实现 Gemini 风格请求校验

`internal/validation/gemini_request.go` 负责：

- 解压请求体
- 解析 JSON
- 检查 `{model}` 非空
- 检查 `generationConfig.imageConfig.output` 或 query 中的 `output=url`
- 统计 prompt 文本长度
- 校验参考图 URL 数量、协议、单 URL 长度
- 返回规范化后的 `request_body_json`

### Step 4: 运行单元测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/security ./internal/validation -count=1
```

Expected: 所有验证与安全辅助测试 PASS。

### Step 5: Commit

```bash
git add async-gateway
git commit -m "feat: add request validation and security helpers"
```

---

## Task 3: 建 PostgreSQL schema、迁移器与仓储层

**Files:**
- Create: `async-gateway/migrations/0001_init.up.sql`
- Create: `async-gateway/migrations/0001_init.down.sql`
- Create: `async-gateway/cmd/banana-async-migrate/main.go`
- Create: `async-gateway/internal/store/postgres.go`
- Create: `async-gateway/internal/store/repository.go`
- Create: `async-gateway/internal/store/repository_test.go`
- Modify: `async-gateway/go.mod`

### Step 1: 先写 repository 合约测试

使用 `pgxmock` 为以下方法写测试：

- `CreateAcceptedTask`
- `MarkQueued`
- `MarkRunning`
- `FinishSucceeded`
- `FinishFailed`
- `MarkUncertain`
- `GetTaskByID`
- `ListTasksByOwner`
- `FindRecoverableTasks`
- `DeleteExpiredTasksBatch`
- `DeleteExpiredPayloadsBatch`

重点校验：

- `updated_at = NOW()` 在每个 `UPDATE` 中显式出现
- `Authorization` 不进入 `tasks`
- `request_body_json` 只进入 `task_payloads`
- 列表查询使用 `before_created_at + before_id`

### Step 2: 编写 migrations

`0001_init.up.sql` 创建：

- `tasks`
- `task_payloads`
- 索引：
  - `idx_tasks_owner_created_at`
  - `idx_tasks_status_created_at`
  - `idx_tasks_recovery_scan`
  - `idx_tasks_gc_finished_at`
  - `idx_task_payloads_gc_expires_at`

`0001_init.down.sql` 只回滚首版已建对象，不涉及 `task_events`。

### Step 3: 实现 Postgres 连接与仓储层

- `internal/store/postgres.go`
  - 初始化 `pgxpool.Pool`
  - 连接池参数来自配置
- `internal/store/repository.go`
  - 封装任务摘要与短期 payload 的增删改查
  - 提供提交事务接口，保证 `tasks + task_payloads` 同提交
  - 提供恢复扫描与 TTL 删除查询

### Step 4: 实现迁移命令

`cmd/banana-async-migrate/main.go` 负责：

- 读取 `POSTGRES_DSN`
- 执行 `up` / `down` / `version`
- 供部署或本地开发手动执行

### Step 5: 验证仓储测试与迁移命令可构建

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/store -count=1
go build ./cmd/banana-async-migrate
```

Expected: repository 测试 PASS，迁移命令可构建。

### Step 6: Commit

```bash
git add async-gateway
git commit -m "feat: add postgres schema and repository layer"
```

---

## Task 4: 实现提交端点与入队主路径

**Files:**
- Create: `async-gateway/internal/queue/memory_queue.go`
- Create: `async-gateway/internal/queue/memory_queue_test.go`
- Create: `async-gateway/internal/httpapi/middleware.go`
- Create: `async-gateway/internal/httpapi/submit_handler.go`
- Create: `async-gateway/internal/httpapi/submit_handler_test.go`
- Modify: `async-gateway/internal/httpapi/router.go`
- Modify: `async-gateway/internal/app/app.go`
- Modify: `async-gateway/internal/store/repository.go`

### Step 1: 先写提交 handler 测试

覆盖：

- 缺少 `Authorization` 返回 `401`
- `output != url` 返回 `400`
- body 过大返回 `413`
- 正常提交返回 `202`，含：
  - `id`
  - `status=accepted`
  - `polling_url`
  - `content_url`
- 队列满返回 `503 + Retry-After`
- 写库失败返回 `500`

### Step 2: 实现内存队列与提交事务

- `internal/queue/memory_queue.go`
  - 固定容量 channel 或 ring buffer 包装
  - 暴露 `Enqueue` / `TryEnqueue`
- `submit_handler.go`
  - 解压、校验、派生 `owner_hash`
  - 生成 `task_id`
  - 加密 `Authorization`
  - 调 repository 事务写 `tasks + task_payloads`
  - 成功后入队并把状态推进为 `queued`
  - 队列满则将任务改为 `failed + queue_full`

### Step 3: 接入 router 与应用依赖

- 在 `router.go` 中注册 `POST /v1beta/models/{model}:generateContent`
- 在 `app.go` 中初始化 queue、repository、submit handler

### Step 4: 运行提交路径测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/queue ./internal/httpapi -run 'TestSubmit' -count=1
```

Expected: 提交端点相关测试 PASS。

### Step 5: Commit

```bash
git add async-gateway
git commit -m "feat: add async task submission endpoint"
```

---

## Task 5: 实现 worker pool、NewAPI 转发与终态摘要提取

**Files:**
- Create: `async-gateway/internal/worker/pool.go`
- Create: `async-gateway/internal/worker/pool_test.go`
- Create: `async-gateway/internal/worker/forwarder.go`
- Create: `async-gateway/internal/worker/forwarder_test.go`
- Create: `async-gateway/internal/worker/summary.go`
- Create: `async-gateway/internal/worker/summary_test.go`
- Modify: `async-gateway/internal/app/app.go`
- Modify: `async-gateway/internal/store/repository.go`

### Step 1: 先写 forwarder/summary 测试

使用 `httptest.Server` 模拟 `NewAPI`，覆盖：

- 转发时保留原 path/query/body/auth 主体
- 若提交阶段已解压，转发时移除 `Content-Encoding`
- 正常 `200` 响应提取：
  - `image_urls`
  - `finish_reason`
  - `response_id`
  - `model_version`
  - `usage_metadata`
- `401/403/402/429/5xx` 分类为预期错误码
- 请求已发出后连接中断，标记 `uncertain`

### Step 2: 实现 worker pool

- 固定 worker 数量，配置来自 `MAX_INFLIGHT_TASKS`
- 从队列取任务后：
  - 标记 `running`
  - 刷 `heartbeat_at`
  - 调用 `forwarder`
  - 写成功/失败/不确定终态

### Step 3: 实现 NewAPI 转发与摘要提取

`forwarder.go` 负责：

- 从 `task_payloads` 取规范化 JSON 与 `auth_ciphertext`
- 解密出用户 `Authorization`
- 构造对 `NewAPI` 的请求
- 用 `httptrace.WroteRequest` 写 `request_dispatched_at`
- 分类 `408/429/4xx/5xx/transport` 错误

`summary.go` 负责：

- 从最终 JSON 响应中抽取摘要
- 优先提取可读失败原因
- 不保留整份原始响应

### Step 4: 启动 worker 并接入 app

- `app.go` 在启动时初始化 worker pool
- 优雅停机时先停提交、后等 worker drain

### Step 5: 运行 worker 相关测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/worker -count=1
```

Expected: forwarder、summary、worker pool 测试 PASS。

### Step 6: Commit

```bash
git add async-gateway
git commit -m "feat: add worker pool and newapi forwarding"
```

---

## Task 6: 实现查询端点、热点缓存与轮询限频

**Files:**
- Create: `async-gateway/internal/cache/task_cache.go`
- Create: `async-gateway/internal/cache/task_cache_test.go`
- Create: `async-gateway/internal/ratelimit/limiter.go`
- Create: `async-gateway/internal/ratelimit/limiter_test.go`
- Create: `async-gateway/internal/httpapi/query_handler.go`
- Create: `async-gateway/internal/httpapi/query_handler_test.go`
- Modify: `async-gateway/internal/httpapi/router.go`
- Modify: `async-gateway/internal/app/app.go`

### Step 1: 先写查询接口测试

覆盖：

- `GET /v1/tasks/{id}`：
  - `404`
  - `403`
  - `accepted/queued/running` 返回 `Retry-After`
  - `succeeded` 返回摘要
  - `failed` 返回 `error`
  - `uncertain` 返回 `transport_uncertain`
- `GET /v1/tasks`：
  - 默认 `days=3`
  - `days > 3` 被截断或拒绝
  - `before_created_at + before_id` keyset 分页
- `GET /v1/tasks/{id}/content`：
  - 成功 `302`
  - 头里带 `Referrer-Policy: no-referrer`
  - 非终态返回 `409`
- 高频轮询触发 `429`

### Step 2: 实现本地缓存与限频器

- `task_cache.go`
  - 运行中任务短 TTL
  - 终态任务 `30 ~ 60s`
  - 列表查询 `3 ~ 10s`
- `limiter.go`
  - 按 `task_id + owner_hash + IP` 限频
  - 先用进程内令牌桶，不引入 Redis

### Step 3: 实现 3 个查询 handler

- `query_handler.go`
  - 先鉴权、派生 `owner_hash`
  - 再限频
  - 缓存 miss 时查库
  - 列表接口只回摘要字段
  - `content` 端点只从 `result_summary_json.image_urls[0]` 取 URL

### Step 4: 接入 router 并更新 app 依赖

- 注册：
  - `GET /v1/tasks/{id}`
  - `GET /v1/tasks`
  - `GET /v1/tasks/{id}/content`
- 将 cache、limiter 注入 handlers

### Step 5: 运行查询路径测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/cache ./internal/ratelimit ./internal/httpapi -run 'Test(GetTask|ListTasks|TaskContent)' -count=1
```

Expected: 查询、分页、302 跳转与限流测试 PASS。

### Step 6: Commit

```bash
git add async-gateway
git commit -m "feat: add task query endpoints and polling protections"
```

---

## Task 7: 实现恢复扫描、TTL 清理、优雅停机与基础 metrics

**Files:**
- Create: `async-gateway/internal/recovery/scanner.go`
- Create: `async-gateway/internal/recovery/scanner_test.go`
- Create: `async-gateway/internal/cleanup/cleanup.go`
- Create: `async-gateway/internal/cleanup/cleanup_test.go`
- Create: `async-gateway/internal/metrics/metrics.go`
- Create: `async-gateway/internal/app/lifecycle.go`
- Modify: `async-gateway/internal/app/app.go`
- Modify: `async-gateway/internal/store/repository.go`

### Step 1: 先写恢复与清理测试

覆盖：

- `accepted / queued` 任务在重启后被重新入队
- `running + request_dispatched_at IS NULL` 被重新入队
- `running + request_dispatched_at IS NOT NULL` 被标记 `uncertain`
- 缺少 `task_payloads` 时标记 `failed + recovery_payload_missing`
- TTL 删除只删已终态且过期数据
- 非终态且尚未真正发出的 payload 不会被误删

### Step 2: 实现恢复扫描

- 启动时执行一次 `FindRecoverableTasks`
- 按设计文档区分：
  - 可重放
  - 必须 `uncertain`
  - `recovery_payload_missing`
- 打结构化日志：
  - `recovery_requeued`
  - `recovery_marked_uncertain`

### Step 3: 实现 TTL cleaner

- 小批量删除 `tasks`
- 小批量删除 `task_payloads`
- 每次循环可配置 batch size 与间隔
- 使用 `FOR UPDATE SKIP LOCKED` 版本 SQL

### Step 4: 实现优雅停机与基础 metrics

- `app/lifecycle.go`
  - 收到 `SIGTERM/SIGINT`
  - 先让提交端点进入 `service_draining`
  - 查询接口短暂继续服务
  - 超过 `SHUTDOWN_GRACE_PERIOD` 后，把仍在途且已发出的任务条件更新为 `uncertain`
- `metrics.go`
  - 基础 counters/gauges：
    - 提交成功/失败
    - 查询 QPS
    - `429` 数
    - 队列深度
    - worker 忙碌数
    - 终态分布
    - 恢复扫描结果
  - 首版可先用 `expvar` 暴露内部计数

### Step 5: 运行恢复与生命周期测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./internal/recovery ./internal/cleanup ./internal/app -count=1
```

Expected: 恢复、清理、优雅停机相关测试 PASS。

### Step 6: Commit

```bash
git add async-gateway
git commit -m "feat: add recovery cleanup and graceful shutdown"
```

---

## Task 8: 交付包装、部署样例与端到端烟测

**Files:**
- Create: `async-gateway/deploy/banana-async-gateway.env.example`
- Create: `async-gateway/deploy/nginx.async_gateway.conf.example`
- Modify: `async-gateway/Dockerfile`
- Modify: `async-gateway/README.md`
- Modify: `README.md`

### Step 1: 完善交付资料

- `env.example` 写明所有首版配置项
- `nginx.async_gateway.conf.example` 提供：
  - A3 baseUrl 路由到 `async-gateway`
  - 同步流量继续去 `NewAPI`
- `README.md` 解释：
  - A3 与现有 `gemini-worker-go` 的职责分工
  - 本地运行方式
  - 迁移命令
  - 烟测命令

### Step 2: 完善 Dockerfile

要求：

- 多阶段构建
- 独立编译 `banana-async-gateway`
- 默认仅打包 A3 新服务，不影响现有根目录镜像

### Step 3: 运行完整测试

Run:

```bash
cd /home/feng/project/banana-proxy/geminiworker/go-implementation/async-gateway
go test ./... -count=1
go build ./cmd/banana-async-gateway
go build ./cmd/banana-async-migrate
docker build -t banana-async-gateway .
```

Expected: 全部测试 PASS，两个二进制可构建，镜像构建成功。

### Step 4: 手工烟测

在本地或测试环境完成：

1. 执行 migration。
2. 启动 `NewAPI`、`gemini-worker-go`、PostgreSQL、A3 gateway。
3. 用 gzip JSON 请求提交异步任务。
4. 验证立即返回 `202 + task_id`。
5. 连续轮询直到成功，确认：
   - `GET /v1/tasks/{id}` 返回摘要
   - `GET /v1/tasks` 能看到最近 3 天任务
   - `GET /v1/tasks/{id}/content` 返回 `302`
6. 人工重启 A3 gateway，验证任务不丢失。

### Step 5: Commit

```bash
git add async-gateway README.md
git commit -m "docs: add async gateway deployment and smoke test guidance"
```

---

## 验收清单

- 能在 A3 baseUrl 上接受 Gemini 风格请求并返回 `202 + task_id`
- `tasks` 与 `task_payloads` 分离存储，且最近 3 天查询不依赖完整 payload
- worker 能在同机内网长连 `NewAPI`
- 成功场景能返回最终图片 URL 摘要
- 失败场景能返回可读错误原因
- `uncertain`、`queue_full`、`recovery_payload_missing` 等边界状态可见
- `Retry-After`、`429`、热点缓存、列表 cursor 分页有效
- 网关重启后已返回的 `task_id` 不丢
- 优雅停机不会粗暴覆盖已完成终态
- TTL 清理不会误删未真正发出的 payload

## 风险与回滚点

- 若 `pgx/v5` 或迁移器引入后复杂度超预期：
  - 回滚到只保留 migration SQL 文件
  - 暂不引入自动迁移 runner
- 若 `expvar` 指标不足以满足接入：
  - 先保留内部 counters 与结构化日志
  - 二阶段再接 Prometheus client
- 若 `async-gateway/` 独立 module 给部署链路带来阻力：
  - 不改设计，只改仓库布局
  - 可把新服务平移到独立 repo，代码结构保持不变

## 建议执行顺序

1. Task 1
2. Task 2
3. Task 3
4. Task 4
5. Task 5
6. Task 6
7. Task 7
8. Task 8

严格按顺序执行，不要先写 worker 再补提交端点，也不要先做恢复扫描再补基础存储。
