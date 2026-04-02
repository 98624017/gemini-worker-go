# Async Gateway Batch Polling Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为 `async-gateway` 增加批量任务状态查询接口，支撑客户端统一节拍轮询多个并发任务。

**Architecture:** 新增 `POST /v1/tasks/batch-get`，请求体传入多个 task ID，服务端在同一 owner 范围内批量返回任务状态，并提供统一的 `next_poll_after_ms` 建议值。实现上优先复用现有单任务 cache，并新增 repository 批量查询，避免对数据库执行 N 次单条查询。

**Tech Stack:** Go 1.25, net/http, PostgreSQL, testing, httptest

---

### Task 1: 为批量接口输入校验写失败测试

**Files:**
- Modify: `async-gateway/internal/httpapi/query_handler_test.go`

**Step 1: 写缺少 ids 的失败测试**

新增测试：

- 请求体没有 `ids`
- 预期 `400 invalid_request`

**Step 2: 写超过上限的失败测试**

新增测试：

- `ids` 数量超过 `100`
- 预期 `400 invalid_request`

**Step 3: 运行测试确认失败**

Run: `go test ./internal/httpapi -run TestBatchGetTasks -count=1 -timeout 60s`

Expected: FAIL，因为路由和处理器尚未存在

### Task 2: 设计并实现 repository 批量查询

**Files:**
- Modify: `async-gateway/internal/store/repository.go`
- Modify: `async-gateway/internal/store/repository_test.go`

**Step 1: 写 repository 失败测试**

新增测试验证：

- 一次按多个 task ID 批量查询
- 仅返回存在的任务
- 可以保持通过 ID map 回传

**Step 2: 运行单测确认失败**

Run: `go test ./internal/store -run TestRepositoryGetTasksByIDs -count=1 -timeout 60s`

Expected: FAIL，因为方法尚不存在

**Step 3: 实现最小批量查询**

新增：

```go
GetTasksByIDs(ctx context.Context, ids []string) (map[string]*domain.Task, error)
```

要求：

- 空输入直接返回空 map
- 使用单条 SQL 查询
- 只查 `tasks`

**Step 4: 运行测试确认通过**

Run: `go test ./internal/store -run TestRepositoryGetTasksByIDs -count=1 -timeout 60s`

Expected: PASS

### Task 3: 实现批量 query handler

**Files:**
- Modify: `async-gateway/internal/httpapi/query_handler.go`
- Modify: `async-gateway/internal/httpapi/query_handler_test.go`
- Modify: `async-gateway/internal/httpapi/router.go`

**Step 1: 写 handler 行为测试**

测试覆盖：

- 批量返回多个任务状态
- 输入顺序保持
- 非当前 owner 的任务返回 `not_found`
- 成功任务返回 `content_url`
- 返回 `next_poll_after_ms`

**Step 2: 运行测试确认失败**

Run: `go test ./internal/httpapi -run TestBatchGetTasksSuccess -count=1 -timeout 60s`

Expected: FAIL

**Step 3: 实现 handler**

实现点：

- 新增 `BatchGetTasks`
- 解析 JSON 请求体
- 做 `ids` 去重和上限校验
- 先查 cache，再批量查 DB
- 构造批量响应

**Step 4: 接入路由**

新增路由：

```text
POST /v1/tasks/batch-get
```

**Step 5: 运行测试确认通过**

Run: `go test ./internal/httpapi -run TestBatchGetTasks -count=1 -timeout 60s`

Expected: PASS

### Task 4: 限流与缓存验证

**Files:**
- Modify: `async-gateway/internal/httpapi/query_handler_test.go`

**Step 1: 写 batch 限流测试**

验证：

- 相同 owner 和相同 IP 过快轮询会返回 `429`
- 响应带 `Retry-After`

**Step 2: 写 cache 行为测试**

验证：

- task 已在 cache 时不需要命中 repository
- cache miss 时会回填

**Step 3: 运行测试确认通过**

Run: `go test ./internal/httpapi -run 'TestBatchGetTasks(RateLimited|UsesCache)' -count=1 -timeout 60s`

Expected: PASS

### Task 5: 更新文档

**Files:**
- Modify: `async-gateway/README.md`

**Step 1: 补充接口说明**

新增：

- `POST /v1/tasks/batch-get`
- 请求体格式
- 响应格式
- 推荐客户端轮询策略

**Step 2: 补充使用建议**

说明：

- 客户端应按统一节拍轮询
- 每轮批量查询 pending 任务
- 终态任务及时移出轮询集合

### Task 6: 全量验证

**Files:**
- Verify: `async-gateway/internal/httpapi/query_handler.go`
- Verify: `async-gateway/internal/store/repository.go`
- Verify: `async-gateway/README.md`

**Step 1: 跑相关测试**

Run: `go test ./internal/httpapi ./internal/store -count=1 -timeout 60s`

Expected: PASS

**Step 2: 跑全量测试**

Run: `go test ./... -count=1 -timeout 60s`

Expected: PASS

**Step 3: 检查 diff**

Run: `git diff --check -- async-gateway/internal/httpapi/query_handler.go async-gateway/internal/httpapi/query_handler_test.go async-gateway/internal/httpapi/router.go async-gateway/internal/store/repository.go async-gateway/internal/store/repository_test.go async-gateway/README.md docs/plans/2026-03-22-async-gateway-batch-polling-design.md docs/plans/2026-03-22-async-gateway-batch-polling.md`

Expected: 无 patch/空白问题

**Step 4: 提交**

```bash
git add async-gateway/internal/httpapi/query_handler.go async-gateway/internal/httpapi/query_handler_test.go async-gateway/internal/httpapi/router.go async-gateway/internal/store/repository.go async-gateway/internal/store/repository_test.go async-gateway/README.md docs/plans/2026-03-22-async-gateway-batch-polling-design.md docs/plans/2026-03-22-async-gateway-batch-polling.md
git commit -m "feat: add async-gateway batch polling api"
```
