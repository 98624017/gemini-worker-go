# 性能优化设计文档

**日期：** 2026-03-17
**方向：** 性能 / 吞吐
**方案：** B — 引入 json-iterator + sync.Pool buffer 复用 + L1 内存 LRU + SSE buffer pool

---

## 背景

当前项目为 stdlib-only 单二进制 Go 代理服务（`go.mod` 无任何 `require`）。
随着并发请求增长，发现以下性能瓶颈：

| 瓶颈 | 位置 | 影响 |
|------|------|------|
| `map[string]interface{}` JSON round-trip（64 处） | main.go | 每请求两次大对象分配 |
| `io.ReadAll` 全量读入 body | 请求入口 | 大 body（含 base64 图）内存峰值高 |
| 无 bytes.Buffer 复用 | base64 编码、JSON 序列化 | GC 压力，高并发延迟抖动 |
| 磁盘 LRU 缓存无内存 L1 | inline_data_url_cache.go | 热点图片每次仍走磁盘 I/O |
| SSE scanner buffer 每连接分配 ~17MB | handleStreamResponse | 高并发时内存峰值高 |

---

## 决策：突破 stdlib-only 约束

### 为什么要突破

`encoding/json` 在 `map[string]interface{}` 场景下性能有限，
是本服务最大的 CPU + 内存分配来源。

### 引入什么

`github.com/json-iterator/go`（纯 Go，无 CGO，MIT 协议，维护活跃）

- **好处：** JSON 解析/序列化 1.5–2x，drop-in 替换，零代码改动
- **坏处：** 引入 go.sum 维护负担；依赖本身 bug 需跟进

### 不引入什么

不引入 `bytedance/sonic`（需要 CGO，arm64 性能打折，部署更复杂）。

---

## 改动清单（4 个独立任务）

### ① 引入 json-iterator

**文件：** `go.mod`、`main.go`

```go
// go.mod
require github.com/json-iterator/go v1.1.12

// main.go — 顶部新增，替换所有 json.XXX 调用
import jsoniter "github.com/json-iterator/go"
var json = jsoniter.ConfigCompatibleWithStandardLibrary
```

所有现有 `json.Marshal` / `json.Unmarshal` / `json.NewDecoder` 无需修改。

**预期收益：** JSON 解析/序列化 1.5–2x 提速，对重度 base64 body 尤为显著。

---

### ② sync.Pool 复用 bytes.Buffer

**文件：** `main.go`

```go
var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}
```

改写约 6 处 `json.Marshal(bodyMap)` → `json.NewEncoder(buf).Encode(bodyMap)`：

```go
buf := bufPool.Get().(*bytes.Buffer)
buf.Reset()
defer bufPool.Put(buf)
if err := json.NewEncoder(buf).Encode(v); err != nil { ... }
result := buf.Bytes()
```

**预期收益：** 减少高频小对象分配，降低 GC STW 频率，高并发下 P99 延迟更稳。

---

### ③ L1 内存 LRU（inline data URL 缓存）

**文件：** `inline_data_url_cache.go`

在现有磁盘缓存前新增纯内存层（`container/list` + `sync.RWMutex`，stdlib only）：

```
请求
  → L1 内存查（RLock）→ 命中 → 直接返回
                      ↓ miss
                 L2 磁盘查 → 命中 → 写回 L1 → 返回
                      ↓ miss
                 网络拉取 → 写 L2 磁盘 → 写 L1 → 返回
```

**数据结构：**
```go
type memCacheEntry struct {
    mime      string
    data      []byte
    size      int64
    accessedAt time.Time
    elem      *list.Element
}

type inlineDataURLMemCache struct {
    mu       sync.RWMutex
    items    map[string]*memCacheEntry
    lru      *list.List
    maxBytes int64
    curBytes int64
}
```

**新增环境变量：**
- `INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES`（默认 `104857600`，即 100MiB）
- 设为 `0` / `off` 禁用（磁盘缓存 L2 仍正常工作）

**预期收益：** 热点图片（同一图片在短时间内多次请求）命中率接近 100%，
完全消除磁盘 I/O（通常 1–5ms），总延迟降低明显。

---

### ④ SSE scanner buffer pool

**文件：** `main.go`（`handleStreamResponse` 函数）

```go
var sseScannerBufPool = sync.Pool{
    New: func() any {
        b := make([]byte, MaxSSEScanTokenBytes)
        return &b
    },
}

// 在 handleStreamResponse 中：
bufPtr := sseScannerBufPool.Get().(*[]byte)
scanner.Buffer(*bufPtr, MaxSSEScanTokenBytes)
defer sseScannerBufPool.Put(bufPtr)
```

**预期收益：**
`MaxSSEScanTokenBytes ≈ 17MB`，每个 SSE 连接从"每次分配"变为"复用"，
10 并发 SSE 连接峰值内存降低约 170MB。

---

## 测试策略

- 现有测试套件（`go test ./...`）全部通过作为回归基线
- json-iterator 替换后用现有 `main_test.go` 中的 JSON round-trip 测试覆盖
- L1 cache 新增单元测试：命中、淘汰、并发读写、容量上限
- SSE buffer pool 依赖现有 SSE 集成测试

---

## 风险与降级

| 风险 | 降级方案 |
|------|---------|
| json-iterator 行为与 stdlib 不一致 | `ConfigCompatibleWithStandardLibrary` 模式最大化兼容；出问题直接删 import 回滚 |
| L1 cache 内存占用超预期 | 默认 100MiB 上限；设 `INLINE_DATA_URL_MEMORY_CACHE_MAX_BYTES=0` 关闭 |
| SSE buffer pool 并发 unsafe | `sync.Pool` 是 goroutine-safe，无风险 |

---

## 实施顺序

1. ② sync.Pool buffer（最小，独立，可先合）
2. ① json-iterator（修改 go.mod，需 `go mod tidy`）
3. ④ SSE scanner buffer pool（小改，独立）
4. ③ L1 内存 LRU（最复杂，独立文件改动）
