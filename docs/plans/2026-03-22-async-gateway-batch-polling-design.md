# Async Gateway Batch Polling Design

## 背景

当前 `async-gateway` 面向客户端提供的是单任务轮询模型：

- `GET /v1/tasks/{id}`
- `GET /v1/tasks/{id}/content`
- `GET /v1/tasks`

这套模型在少量任务下足够直接，但如果后续客户端会发起大量图片生成并发请求，
单任务逐个轮询会带来两个问题：

1. 客户端侧维护大量定时器，节拍分散，容易形成“抖动式”流量
2. 服务端收到大量小而频繁的短请求，请求数远高于真正需要传输的状态数据量

同时，客户端运行环境不可靠，因此不能把长连接推送（SSE / WebSocket）
作为主方案。

## 目标

为后续自研客户端和 `async-gateway` 增加一套更适合高并发异步任务的状态同步方式：

- 一次请求可查询多个任务状态
- 客户端可把多个进行中的任务对齐到统一轮询节拍
- 保持短连接、幂等、易恢复
- 不依赖客户端长连接稳定性

## 非目标

本次不做：

- SSE / WebSocket 主通道
- 按“任务变更流”做增量同步游标
- 推送回调或 webhook
- 改动现有单任务查询接口的兼容行为

## 方案比较

### 方案 A：客户端只做“对齐轮询”，服务端不改

客户端把所有 pending 任务按固定时间片归桶，再并行调用现有
`GET /v1/tasks/{id}`。

优点：

- 服务端改动最少

缺点：

- 请求总数不变
- 每个任务仍然需要单独握手
- 服务端连接开销与限流压力并没有本质下降

### 方案 B：批量状态查询接口

服务端新增一条批量查询接口，客户端每轮把待查任务 ID 一次提交给服务端。

优点：

- 一次请求查询多个任务
- 最适合不稳定客户端
- 短连接、幂等、恢复简单
- 明显降低请求数和握手次数

缺点：

- 需要新增接口、限流和测试

### 方案 C：增量同步接口

客户端按“自上次同步后有什么变化”而不是按任务 ID 查询。

优点：

- 在超大规模并发任务下更省流量

缺点：

- 复杂度明显更高
- 需要引入游标、顺序、补偿和过期策略

## 推荐方案

选择 **方案 B：批量状态查询接口**。

原因：

- 最贴合“客户端环境不可控”的前提
- 能立刻降低请求数
- 保持短连接和幂等语义
- 不需要先引入复杂事件流模型

## 接口设计

新增：

```text
POST /v1/tasks/batch-get
```

请求头：

- `Authorization: Bearer <api-key>`
- `Content-Type: application/json`

请求体：

```json
{
  "ids": ["img_a", "img_b", "img_c"]
}
```

约束：

- 单次最多 `100` 个任务 ID
- 空数组非法
- 重复 ID 允许传入，但服务端会按首次出现顺序去重
- 只能查询当前 `owner_hash` 下的任务

响应示例：

```json
{
  "object": "batch.task.list",
  "items": [
    {
      "id": "img_a",
      "status": "running"
    },
    {
      "id": "img_b",
      "status": "succeeded",
      "content_url": "/v1/tasks/img_b/content"
    },
    {
      "id": "img_c",
      "status": "failed",
      "error": {
        "code": "upstream_timeout",
        "message": "upstream request timed out"
      }
    }
  ],
  "next_poll_after_ms": 3000
}
```

设计要点：

- 返回项顺序与客户端输入顺序一致
- 终态任务直接返回足够信息，避免客户端再追加单任务查询
- 对 pending 状态返回统一的 `next_poll_after_ms`

## 访问控制与错误语义

### 认证

沿用现有 `Authorization -> owner_hash` 机制。

### 越权查询

如果某个任务不属于当前 API Key，对客户端返回 `not_found`，
避免泄露任务存在性。

### 输入错误

- 缺少 `ids`：`400`
- `ids` 非数组：`400`
- 超过上限：`400`

### 轮询限流

保留现有按 `owner_hash + clientIP` 的速率限制思路，但将 scope 从单任务扩展为：

```text
batch:<owner_hash>:<ip>
```

## 服务端实现建议

### 查询层

在 `query_handler` 中新增 `BatchGetTasks` 处理器。

### repository 层

建议新增批量查询方法，而不是循环调用 `GetTaskByID`：

```go
GetTasksByIDs(ctx context.Context, ids []string) (map[string]*domain.Task, error)
```

这样可以把一次批量轮询压缩成一次数据库查询。

### cache 层

继续复用现有单任务 cache：

- 先逐个查 task cache
- 只把 cache miss 的任务打到数据库
- 数据库结果回填 cache

不需要新增复杂的 batch cache。

## 客户端建议策略

客户端维护所有 pending 任务集合。

每个轮询周期：

1. 收集当前所有 `accepted / queued / running` 的任务
2. 按固定节拍，例如每 `3s` 一次
3. 把待查任务 ID 合并成一个或多个 `batch-get` 请求
4. 拿到结果后：
   - `succeeded / failed / uncertain` 从轮询集合移除
   - `accepted / queued / running` 留到下一轮
5. 使用服务端返回的 `next_poll_after_ms` 作为下一轮基础节拍

这样可以把大量分散轮询收敛成少量整点短连接请求。

## 为什么不优先做 SSE

在“客户端环境不可控、不可靠”的条件下，SSE / WebSocket 的恢复、断线重连、
前后台切换、网络抖动兼容都更复杂。批量短轮询更适合作为主方案。

SSE 可以作为未来增强方向，但不应是首发主通道。

## 测试

需要覆盖：

1. 批量输入校验
2. 任务归属校验
3. 输入顺序保持
4. 终态任务内容字段返回
5. cache miss / hit 混合场景
6. 限流响应

## 结论

对当前项目而言，最合适的减轻轮询压力方案是：

- 服务端新增 `POST /v1/tasks/batch-get`
- 客户端统一轮询节拍
- 多任务一次查询

这能在不依赖长连接稳定性的前提下，显著降低请求数与握手压力。
