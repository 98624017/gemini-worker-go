# 香蕉Pro 图像生成异步化三案对比

**对比对象：** `v4.0` vs `A2-CF` vs `A3-LocalGateway-HC`  
**用途：** 方案评审 / 三案拍板 / 会前统一口径  
**结论定位：** 决策版摘要，不替代详细设计文档

---

## 一、结论先说

如果要一个最短答案：

- **短期最保守止血：** `v4.0`
- **长期云上任务化：** `A2-CF`
- **同机部署且想从一开始就考虑高并发：** `A3-LocalGateway-HC`

在你当前新增的前提下：

- `Go` 中间层与 `NewAPI` 同机
- 已有 PostgreSQL 服务实例
- 希望从一开始就考虑高并发

我的优先级会改成：

1. **A3-LocalGateway-HC**
2. `v4.0`
3. `A2-CF`

---

## 二、三条路线一句话定义

### v4.0

客户端先打 `NewAPI`，`NewAPI` 把 `CF` 当作 Sora 风格异步上游来轮询状态。

### A2-CF

客户端先打 `CF`，`CF` 返回 `task_id`，`Workflow` 代用户直连 `NewAPI` 同步等待结果。

### A3-LocalGateway-HC

客户端先打同机 `Go` 异步网关，网关立即返回 `task_id`，后台通过本机长连接调用 `NewAPI`，状态持久化在 PostgreSQL，并完整保留最近 `3 天` 任务状态。

---

## 三、核心差异总表

| 维度 | v4.0 | A2-CF | A3-LocalGateway-HC |
|---|---|---|---|
| 首次提交入口 | `NewAPI` | `CF` | `Go Async Gateway` |
| 后续轮询发起方 | `NewAPI` | 客户端 | 客户端 |
| 客户端长连接压力 | 仍部分依赖 `NewAPI` | 基本切掉 | 基本切掉 |
| 长等待链路 | `NewAPI -> CF` 轮询机制 | `CF -> NewAPI` 公网长连接 | `Go -> NewAPI` 本机/内网长连接 |
| 状态真相源 | `KV` 为主，需兜底 | `DO` | `PostgreSQL` |
| 最近 `3 天` 查询后端 | `NewAPI` / 其体系 | `CF` 生态层 | `Go 网关 + PostgreSQL` |
| `CF` 是否持有用户 API Key | 否 | 是 | 否 |
| 中间层是否持有用户 API Key | 否 | `CF` 持有 | `Go` 网关持有 |
| 对外协议自然度 | 借壳 Sora | 图片任务协议 | 图片任务协议 |
| 高并发扩展性 | 中 | 中高 | 高 |
| 基础设施复杂度 | 中 | 高 | 中 |
| 安全边界复杂度 | 中 | 高 | 中 |
| 同机部署友好度 | 一般 | 一般 | 最好 |

---

## 四、哪个方案最稳

如果只看“长连接稳定性”：

1. **A3-LocalGateway-HC**
2. `A2-CF`
3. `v4.0`

原因非常简单：

- `A3` 把最长连接放在本机或内网
- `A2` 把最长连接放在 `CF -> 源站` 公网链路
- `v4.0` 则更多依赖 `NewAPI` 现有轮询链路

所以在“客户端弱网是主矛盾”的前提下，`A3` 是最直接的解法。

---

## 五、哪个方案最安全

如果只看“敏感信息边界”：

1. **v4.0**
2. `A3-LocalGateway-HC`
3. `A2-CF`

原因：

- `v4.0` 不需要 `CF` 持有用户 API Key
- `A3` 虽然要短期持有用户 API Key，但只在你自己的机器里
- `A2` 需要把用户 API Key 带进 `CF` 执行环境

---

## 六、哪个方案最适合高并发

如果你已经明确要从一开始就考虑高并发，我的排序是：

1. **A3-LocalGateway-HC**
2. `A2-CF`
3. `v4.0`

### A3 为什么最适合

- `PostgreSQL` 比 `SQLite` 更适合作为高并发状态源
- 不需要承受 `KV` 最终一致问题
- 不需要受 `Workflow payload` 限制
- 可以本地 worker pool + PostgreSQL + 内存缓存逐步演进
- 最近 `3 天` 任务查询可以直接复用 `Go 网关 + PostgreSQL`

### A2 为什么排第二

- `DO` 也能做强一致状态源
- 但 `CF` 整体模型更适合云上异步编排，不如同机方案直接
- 还要承担 `CF -> NewAPI` 公网链路与平台边界

### v4.0 为什么排第三

- 适合保守落地
- 但它的协议和状态设计不是为“高并发任务系统”最优形态

---

## 七、哪条路线最容易首版落地

### 如果你不想新开服务

- `v4.0` 最容易

### 如果你能接受新增一个 Go 网关服务

- `A3` 实际上比 `A2` 更容易

原因：

- 你不需要再引入 `CF Workflows / DO / R2`
- 你已经有机器、`NewAPI`、PostgreSQL
- 排障路径都在自己机器上

---

## 八、PostgreSQL 是否共用

在 A3 里，我的建议是：

- **共用 PostgreSQL 服务实例**
- **不共用数据库边界**
- **当前口径不是 `CF D1` 混合查询版，而是 PostgreSQL 完整保留最近 `3 天`**

也就是：

- 同一个 `postgres` 服务进程可继续用
- 但异步网关应使用：
  - 独立数据库
  - 独立用户
  - 独立连接池预算

这样既节省资源，也能避免异步任务把 `NewAPI` 现有业务库拖慢。

---

## 九、适用场景判断

### 适合 v4.0

- 你现在要的是最保守方案
- 你最怕的是安全边界变复杂
- 你想尽量复用 `NewAPI` 现有异步轮询思路

### 适合 A2-CF

- 你希望任务体系构建在 `CF` 生态上
- 你未来看重边缘能力、云上编排、多地域接入
- 你愿意接受 `CF` 短期持有用户 API Key

### 适合 A3-LocalGateway-HC

- `Go` 网关与 `NewAPI` 已同机或同内网
- 你想把最长等待放在最稳的链路上
- 你已经有 PostgreSQL 服务实例可复用
- 你从一开始就要面向高并发设计
- 你后续即使做网页，也接受“前端可放 CF，后端查询仍走 Go 网关”

---

## 十、推荐结论

在你目前新增的前提下，我的建议已经很明确：

### 第一推荐：A3-LocalGateway-HC

因为它同时满足：

- 客户端不长连
- 同机链路更稳
- 协议更自然
- PostgreSQL 更适合高并发状态持久化
- 最近 `3 天` 任务状态可以完整保留并直接提供查询
- 不需要额外引入一整套 `CF` 状态基础设施

### 第二推荐：v4.0

如果你后面因为安全或组织原因，不想引入新的网关服务，`v4.0` 仍然是最保守备选。

### 第三推荐：A2-CF

它不是坏方案，但在“同机部署 + 已有 PostgreSQL + 高并发优先”这个上下文里，不再是最优。

---

## 十一、对应详细文档

- `v4.0`：
  [香蕉Pro图像生成异步化v4_0.md](/home/feng/project/banana-proxy/geminiworker/go-implementation/香蕉Pro图像生成异步化v4_0.md)

- `A2-CF`：
  [2026-03-19-banana-image-async-a2-do-design.md](/home/feng/project/banana-proxy/geminiworker/go-implementation/docs/plans/2026-03-19-banana-image-async-a2-do-design.md)

- `A3-LocalGateway-HC`：
  [2026-03-19-banana-image-async-a3-local-gateway-hc-design.md](/home/feng/project/banana-proxy/geminiworker/go-implementation/docs/plans/2026-03-19-banana-image-async-a3-local-gateway-hc-design.md)
