# Banana Async Gateway

`async-gateway/` 是 A3 Async Local Gateway 的独立 Go 子项目。

职责拆分：

- `async-gateway`
  负责异步受理、状态持久化、后台 worker 转发、任务查询、恢复扫描、TTL 清理与优雅停机。
- `NewAPI`
  继续负责鉴权、额度和账务。
- 根目录 `gemini-worker-go`
  继续作为 `NewAPI` 下游适配层，处理 URL 型 `inlineData` 转 base64、`output=url` 等同步链路逻辑。

## 工具链

- Go `1.25.8+`
- Docker `24+`（用于镜像构建）
- PostgreSQL `14+`

## 本地运行

1. 准备环境变量：

```bash
cp deploy/banana-async-gateway.env.example /tmp/banana-async-gateway.env
```

2. 填写以下关键值：

- `NEWAPI_BASE_URL`
- `POSTGRES_DSN`
- `OWNER_HASH_SECRET`
- `TASK_PAYLOAD_ENCRYPTION_KEY`

3. 执行 migration：

```bash
set -a
source /tmp/banana-async-gateway.env
set +a

go run ./cmd/banana-async-migrate up
```

4. 启动服务：

```bash
set -a
source /tmp/banana-async-gateway.env
set +a

go run ./cmd/banana-async-gateway
```

## 环境变量

- `LISTEN_ADDR`
  默认 `:8080`
- `NEWAPI_BASE_URL`
  必填；A3 worker 只替换 base URL，不改原 path/query/body/auth 语义
- `POSTGRES_DSN`
  必填；A3 自己的 PostgreSQL
- `OWNER_HASH_SECRET`
  必填；用于从用户 API Key 派生 `owner_hash`
- `TASK_PAYLOAD_ENCRYPTION_KEY`
  必填；Base64 编码后的 `32` 字节密钥
- `POSTGRES_MAX_OPEN_CONNS`
  默认 `20`
- `POSTGRES_MAX_IDLE_CONNS`
  默认 `10`；在 `pgxpool` 下映射为预热连接下限
- `MAX_INFLIGHT_TASKS`
  默认 `32`
- `MAX_QUEUE_SIZE`
  默认 `256`
- `TASK_POLL_RETRY_AFTER_SEC`
  默认 `10`
- `TASK_POLL_BURST`
  默认 `3`；控制同一个轮询 key 在单个 refill 周期内允许的突发查询次数
- `NEWAPI_REQUEST_TIMEOUT_MS`
  默认 `1200000`
- `SHUTDOWN_GRACE_PERIOD_SEC`
  默认 `30`

当前内置但未开放为环境变量的运行参数：

- 恢复扫描 stale threshold：`5m`
- 清理 batch size：`100`
- 清理间隔：`60s`
- 任务摘要保留期：`72h`

## 任务查询接口

### 单任务状态

```text
GET /v1/tasks/{id}
```

返回体顶层是异步任务资源；当任务成功时，`candidates` 内部继续保持接近
Gemini `generateContent` 的结果风格：

```json
{
  "id": "img_xxx",
  "object": "image.task",
  "model": "gemini-3-pro-image-preview",
  "created_at": 1773964800,
  "status": "succeeded",
  "candidates": [
    {
      "content": {
        "parts": [
          {
            "inlineData": {
              "mimeType": "image/png",
              "data": "https://example.com/final.png"
            }
          }
        ]
      },
      "finishReason": "STOP"
    }
  ]
}
```

### 批量状态查询

```text
POST /v1/tasks/batch-get
```

请求体：

```json
{
  "ids": ["img_a", "img_b", "img_c"]
}
```

约束：

- `ids` 必须是非空数组
- 单次最多 `100` 个任务 ID
- 重复 ID 会按首次出现顺序去重
- 只能查询当前 `Authorization` 对应 `owner_hash` 下的任务

响应示例：

```json
{
  "object": "batch.task.list",
  "items": [
    {
      "id": "img_a",
      "object": "image.task",
      "model": "gemini-3-pro-image-preview",
      "created_at": 1773964800,
      "status": "running"
    },
    {
      "id": "img_b",
      "object": "image.task",
      "model": "gemini-3-pro-image-preview",
      "created_at": 1773964801,
      "status": "succeeded",
      "candidates": [
        {
          "content": {
            "parts": [
              {
                "inlineData": {
                  "mimeType": "image/png",
                  "data": "https://example.com/final.png"
                }
              }
            ]
          },
          "finishReason": "STOP"
        }
      ]
    },
    {
      "id": "img_c",
      "object": "image.task",
      "status": "not_found",
      "error": {
        "code": "not_found",
        "message": "task not found"
      }
    }
  ],
  "next_poll_after_ms": 10000
}
```

说明：

- `items` 内部字段尽量复用单任务 `GET /v1/tasks/{id}` 的返回风格
- `not_found` 会同时覆盖“不存在”和“不属于当前 owner”的情况，避免泄露任务存在性
- 客户端应把多个 pending 任务对齐到统一节拍，每轮仅批量查询仍在进行中的任务
- 终态任务应及时移出轮询集合
- `next_poll_after_ms` 是服务端建议的下一轮轮询间隔

## Docker 构建

```bash
docker build -t banana-async-gateway .
```

镜像当前同时打包：

- `banana-async-gateway`
- `banana-async-migrate`
- `migrations/`

默认入口仍然是 `banana-async-gateway`，因此不会影响正常服务启动；如果需要
独立执行 migration，可以覆盖启动命令为 `banana-async-migrate up`。

## 烟测命令

### 自动烟测

新增了两种自动化入口：

- `go run ./cmd/banana-async-smoke`
  适用于已经有运行中的本地 A3 gateway
- `./scripts/run_live_smoke.sh`
  适用于本机临时起一个 PostgreSQL + 本地 A3 gateway，然后对真实 `NewAPI` 发起一次完整异步调用
- `bash ./scripts/run_live_smoke_test.sh`
  适用于离线校验烟测脚本自身的就绪守卫逻辑，确保端口冲突或网关提前退出时不会误报成功

一键端到端烟测示例：

```bash
cd async-gateway

SMOKE_NEWAPI_BASE_URL="https://api.xinbaoai.com" \
SMOKE_API_KEY="<newapi-user-api-key>" \
./scripts/run_live_smoke.sh
```

如果你要用完整请求体而不是默认 prompt，可以把 JSON 保存成文件，再透传给烟测：

```bash
cd async-gateway

SMOKE_NEWAPI_BASE_URL="https://api.xinbaoai.com" \
SMOKE_API_KEY="<newapi-user-api-key>" \
SMOKE_BODY_FILE="/tmp/banana-request.json" \
./scripts/run_live_smoke.sh
```

说明：

- 脚本会临时启动一个 `postgres:16-alpine`
- 自动执行 migration
- 自动启动本地 `banana-async-gateway`
- 就绪阶段会校验新启动的 gateway 进程仍存活，避免误探测到占用同端口的旧服务
- 提交一条 gzip JSON 异步请求
- 轮询 `GET /v1/tasks/{id}`
- 验证 `GET /v1/tasks`
- 验证 `GET /v1/tasks/{id}/content`
- 校验 `/v1/tasks/{id}` 里的 `inlineData.data` 确实是 `http/https` 图片 URL，而不是 base64 或其他伪 URL

本地离线回归命令：

```bash
cd async-gateway

bash -n scripts/run_live_smoke.sh scripts/run_live_smoke_lib.sh scripts/run_live_smoke_test.sh
bash scripts/run_live_smoke_test.sh
go test ./cmd/banana-async-smoke ./internal/smoketest -count=1
```

CI 说明：

- 仓库内新增了 `.github/workflows/async-gateway-ci.yml`
- 当 `async-gateway/**` 或该 workflow 本身发生变更时，会自动运行：
  - smoke shell 语法检查
  - smoke shell 回归测试
  - `banana-async-smoke` 与 `internal/smoketest` 的 Go 测试
  - `async-gateway` 全量 `go test ./...`

## GHCR 镜像发布

仓库根目录新增了 `.github/workflows/async-gateway-ghcr.yml`，用于把
`async-gateway` 的 Docker 镜像发布到 GHCR。

触发规则：

- `push` 到 `main` 时自动构建并推送
- 推送 `v*` tag 时自动构建并推送正式版本
- 支持在 GitHub Actions 页面手动 `workflow_dispatch`

镜像名固定为：

```text
ghcr.io/<owner>/banana-async-gateway
```

标签规则：

- `main` 分支推送：
  - `main`
  - `sha-<shortsha>`
- `v1.2.3` 这类 tag 推送：
  - `v1.2.3`
  - `1.2.3`
  - `1.2`
  - `1`
  - `latest`

权限说明：

- workflow 使用仓库默认 `GITHUB_TOKEN` 登录 GHCR
- 需要仓库 Actions 具备包写入权限；若组织策略收紧，请确认
  `packages: write` 已允许

当前镜像架构：

- 与现有 `Dockerfile` 保持一致，默认发布 `linux/amd64`

拉取示例：

```bash
docker pull ghcr.io/<owner>/banana-async-gateway:main
docker pull ghcr.io/<owner>/banana-async-gateway:v1.2.3
```

## Zeabur 独立 Migration 任务

针对 Zeabur 首次上线，推荐使用“同一镜像 + 临时 migration 服务”的模式。

首次部署步骤：

1. 先准备 PostgreSQL，以及正式服务要用的全部环境变量
2. 新建一个临时 migration 服务，镜像使用：
   `ghcr.io/<owner>/banana-async-gateway:<tag>`
3. 启动命令覆盖为：
   `banana-async-migrate up`
4. 等待日志出现以下任一结果：
   - `banana-async-migrate ... migrate up complete`
   - `banana-async-migrate ... no change`
5. migration 成功后删除这个临时服务
6. 再部署正常版 `banana-async-gateway` 服务

注意事项：

- migration 服务不需要暴露端口
- migration 服务与正式服务必须使用同一个 `POSTGRES_DSN`
- 如果 `POSTGRES_DSN` 指向了错误数据库，migration 虽然能执行，但正式服务仍会查不到预期数据
- 后续版本如果没有新增 migration，可以直接升级正式服务
- 后续版本如果新增 migration，先跑一次临时 migration 服务，再升级正式服务

可选环境变量：

- `SMOKE_GATEWAY_ADDR`
  默认 `127.0.0.1:18080`
- `SMOKE_PG_PORT`
  默认 `55432`
- `SMOKE_MODEL`
  默认 `gemini-3-pro-image-preview`
- `SMOKE_PROMPT`
  默认香蕉图片提示词
- `SMOKE_BODY_FILE`
  可选。若设置，则直接读取完整 JSON 请求体文件并提交；设置后会覆盖默认 `SMOKE_PROMPT`
- `SMOKE_TIMEOUT_SEC`
  默认 `600`
- `SMOKE_POLL_INTERVAL_SEC`
  默认 `10`

前提：

- PostgreSQL、`NewAPI`、根目录 `gemini-worker-go`、A3 gateway 已启动
- A3 base URL 指向 `async-gateway`

1. 提交 gzip JSON 异步任务：

```bash
cat >/tmp/a3-request.json <<'JSON'
{
  "contents": [
    {
      "parts": [
        {
          "text": "draw a yellow banana with studio lighting"
        }
      ]
    }
  ]
}
JSON

gzip -c /tmp/a3-request.json >/tmp/a3-request.json.gz

curl -sS \
  -X POST "http://127.0.0.1:8080/v1beta/models/gemini-3-pro-image-preview:generateContent?output=url" \
  -H "Authorization: Bearer <newapi-user-api-key>" \
  -H "Content-Type: application/json" \
  -H "Content-Encoding: gzip" \
  --data-binary @/tmp/a3-request.json.gz
```

2. 轮询状态：

```bash
curl -sS \
  -H "Authorization: Bearer <newapi-user-api-key>" \
  "http://127.0.0.1:8080/v1/tasks/<task_id>"
```

3. 查看最近 3 天任务：

```bash
curl -sS \
  -H "Authorization: Bearer <newapi-user-api-key>" \
  "http://127.0.0.1:8080/v1/tasks?days=3&limit=20"
```

4. 跳转到最终图片：

```bash
curl -i \
  -H "Authorization: Bearer <newapi-user-api-key>" \
  "http://127.0.0.1:8080/v1/tasks/<task_id>/content"
```

5. 恢复验证：

- 在任务处于 `accepted / queued / running` 时重启 A3 gateway
- 重启后继续轮询同一个 `task_id`
- 确认任务没有丢失，且 `uncertain / recovery_payload_missing` 等边界状态可见
