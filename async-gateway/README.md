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
  默认 `3`
- `NEWAPI_REQUEST_TIMEOUT_MS`
  默认 `660000`
- `SHUTDOWN_GRACE_PERIOD_SEC`
  默认 `30`

当前内置但未开放为环境变量的运行参数：

- 恢复扫描 stale threshold：`5m`
- 清理 batch size：`100`
- 清理间隔：`60s`
- 任务摘要保留期：`72h`

## Docker 构建

```bash
docker build -t banana-async-gateway .
```

镜像只打包 `banana-async-gateway` 运行二进制，不影响根目录现有镜像。

## 烟测命令

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
