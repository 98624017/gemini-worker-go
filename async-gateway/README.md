# Banana Async Gateway

`async-gateway/` 是 A3 Local Gateway 的独立 Go 子项目。

当前阶段仅完成 Task 1 骨架：

- 独立 Go module
- 配置解析与默认值
- 最小 HTTP 路由骨架
- 应用启动与优雅停机入口
- Docker 构建骨架

## 工具链要求

- Go `1.25.8+`
- Docker 构建镜像使用 `golang:1.25.8-alpine`

## 本地运行

```bash
export NEWAPI_BASE_URL=http://newapi:3000
export POSTGRES_DSN='postgres://user:pass@postgres:5432/banana_async_gateway?sslmode=disable'
export OWNER_HASH_SECRET=replace-me
export TASK_PAYLOAD_ENCRYPTION_KEY='MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY='

go run ./cmd/banana-async-gateway
```

## 当前环境变量

- `LISTEN_ADDR`
  默认 `:8080`
- `NEWAPI_BASE_URL`
  必填
- `POSTGRES_DSN`
  必填
- `OWNER_HASH_SECRET`
  必填
- `TASK_PAYLOAD_ENCRYPTION_KEY`
  必填，必须是 Base64 编码后的 `32` 字节密钥
- `MAX_INFLIGHT_TASKS`
  默认 `32`
- `MAX_QUEUE_SIZE`
  默认 `256`
- `TASK_POLL_RETRY_AFTER_SEC`
  默认 `3`
- `POSTGRES_MAX_OPEN_CONNS`
  默认 `20`
- `POSTGRES_MAX_IDLE_CONNS`
  默认 `10`；在 `pgxpool` 下映射为预热连接下限
- `NEWAPI_REQUEST_TIMEOUT_MS`
  默认 `660000`
- `SHUTDOWN_GRACE_PERIOD_SEC`
  默认 `30`
