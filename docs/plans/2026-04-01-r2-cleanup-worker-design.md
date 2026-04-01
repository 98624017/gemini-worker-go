# R2 定时清理 Worker 设计

**目标**

为当前仓库独占的 R2 bucket 增加一个独立的 Cloudflare Worker 定时清理器，
按可配置的时间阈值删除过期对象，默认每 30 分钟执行一次，默认删除上传时间
超过 3 小时的对象。

该能力必须满足以下约束：

- 不把清理逻辑塞进 Go 业务实例
- 不依赖 R2 Lifecycle 的按天过期模型
- 支持多实例业务部署场景，不引入实例间抢占
- 默认只清理当前业务对象前缀，避免未来误删
- 频率与过期阈值都可配置

---

## 背景

当前仓库已经支持把 `output=url` 的图片写入 Cloudflare R2，相关配置为：

- `R2_BUCKET`
- `R2_PUBLIC_BASE_URL`
- `R2_OBJECT_PREFIX`

上传逻辑位于 `main.go`，对象 key 结构为：

```text
<R2_OBJECT_PREFIX>/YYYY/MM/DD/<timestamp>-<rand>.<ext>
```

这类对象天然适合做短时保留，但 Cloudflare R2 Lifecycle 更适合按“天”做
过期管理，无法满足“超过 3 小时尽快删除”的目标。Cloudflare 官方文档说明，
生命周期规则在对象过期后通常会在 24 小时内完成删除，因此不适合作为本需求
的主要手段。

因此本次设计采用：

- **Cloudflare Worker Cron Trigger** 负责调度
- **R2 Workers Binding** 负责列举与删除对象
- **对象 `uploaded` 时间** 作为唯一删除依据

---

## 设计原则

1. 清理职责与主业务完全解耦
2. 删除范围默认收敛到 `R2_OBJECT_PREFIX`
3. 不新增上传元数据，不改 Go 上传协议
4. 允许幂等重试，不引入分布式锁
5. 优先保证可观测性和误删可控性

---

## 方案对比

### 方案 A：R2 Lifecycle

优点：

- Cloudflare 原生能力
- 无需编写 Worker 逻辑

缺点：

- 主要按天配置
- 删除延迟通常为过期后 24 小时内
- 不适合 3 小时级别的精细控制

### 方案 B：Go 服务内置定时清理

优点：

- 代码都留在同一技术栈

缺点：

- 多实例部署时需要选主或锁
- 清理职责污染业务进程
- 运行频率和失败重试更难独立管理

### 方案 C：独立 Cloudflare Worker + Cron + R2 Binding

优点：

- 调度天然集中，不受 Go 实例数影响
- 与 Cloudflare 存储同生态，部署边界清晰
- 可按分钟级执行
- 仅需遍历指定前缀，性能和风险都可控

缺点：

- 仓库中新增一套 Worker 配置与测试

### 结论

采用 **方案 C**。它最符合“独立利用 CF 生态”的目标，也最适合当前
多实例业务部署场景。

---

## 总体架构

新增一个独立子目录，例如：

```text
cloudflare/
└── r2-cleaner/
    ├── package.json
    ├── worker.js
    ├── worker.test.js
    └── wrangler.jsonc
```

职责分层如下：

1. `wrangler.jsonc`
   负责 Cron 调度与 R2 bucket 绑定
2. `worker.js`
   负责配置解析、过期判定、分页遍历、批量删除、日志输出
3. `worker.test.js`
   负责纯函数测试与带假 R2 bucket 的调度测试
4. `README.md`
   负责说明部署方式、默认值和验证方式

---

## 配置设计

### 调度配置

执行频率由 `wrangler.jsonc` 中的 `triggers.crons` 控制。

默认值：

```json
["*/30 * * * *"]
```

表示每 30 分钟执行一次。

### Worker 变量

新增以下 Worker 变量：

- `R2_CLEANUP_PREFIX`
  - 默认：`images`
  - 建议与 Go 服务的 `R2_OBJECT_PREFIX` 保持一致
- `R2_CLEANUP_MAX_AGE_SECONDS`
  - 默认：`10800`
  - 即 3 小时

### 默认行为

若未显式配置：

- 调度频率：30 分钟
- 删除前缀：`images`
- 删除阈值：10800 秒

---

## 清理流程

每次 Cron 触发后，Worker 执行以下步骤：

1. 读取当前时间 `now`
2. 解析 `R2_CLEANUP_MAX_AGE_SECONDS`
3. 计算 `cutoff = now - maxAge`
4. 规范化 `R2_CLEANUP_PREFIX`
5. 调用 `bucket.list({ prefix, cursor })` 分页遍历
6. 对每个对象比较 `object.uploaded <= cutoff`
7. 收集待删 key
8. 以最多 1000 个 key 为一批调用 `bucket.delete(keys)`
9. 输出本次统计日志

核心伪代码：

```js
const cutoffMs = Date.now() - maxAgeSeconds * 1000;
let cursor;

do {
  const page = await env.R2_BUCKET.list({ prefix, cursor });
  const expiredKeys = page.objects
    .filter((object) => object.uploaded.getTime() <= cutoffMs)
    .map((object) => object.key);

  for (const batch of chunk(expiredKeys, 1000)) {
    await env.R2_BUCKET.delete(batch);
  }

  cursor = page.truncated ? page.cursor : undefined;
} while (cursor);
```

---

## 删除依据

删除依据固定为 **R2 对象的 `uploaded` 时间**。

不采用以下做法：

- 不读取对象内容
- 不依赖自定义 metadata
- 不在 key 中反解析时间作为主判断依据

这样做的原因：

1. `uploaded` 由 R2 提供，语义稳定
2. 无需改动当前 Go 上传逻辑
3. 不依赖对象命名细节，后续 key 结构微调也不影响清理

---

## 范围控制

默认只清理 `R2_CLEANUP_PREFIX` 前缀下的对象，不扫全桶。

原因：

1. 当前 bucket 虽然独占，但未来扩展时更安全
2. 前缀扫描减少无关对象遍历，提升执行效率
3. 能与现有 `R2_OBJECT_PREFIX` 保持一致，语义明确

前缀规范：

- 自动去掉首尾空格
- 自动去掉首尾 `/`
- 若结果为空，表示扫描全桶

默认实现仍会提供“空前缀扫全桶”的技术能力，但 README 中应明确标注：
只有在确认 bucket 生命周期完全由本清理器负责时才建议这么做。

---

## 幂等与并发策略

本设计不引入锁，不做选主。

原因：

1. Cloudflare Cron 本身是中心化调度
2. 即使出现偶发重试，删除已不存在对象也是可接受的幂等失败
3. 相比锁机制，这里的收益远小于复杂度

处理策略：

- 列表失败：本次任务视为失败，输出错误日志
- 单批删除失败：记录错误并继续后续批次
- 已不存在对象：按删除失败处理，但不需要额外补偿

---

## 可观测性设计

每次执行至少输出一条汇总日志，包含：

- `run_at`
- `cron`
- `prefix`
- `max_age_seconds`
- `cutoff`
- `listed_count`
- `expired_count`
- `deleted_count`
- `delete_error_count`
- `page_count`

必要时对删除失败批次输出单独错误日志，包含：

- `prefix`
- `batch_size`
- `first_key`
- `error`

这样可以在 Cloudflare Workers 后台直接定位问题，不需要额外接监控系统。

---

## 测试策略

### 单元测试

对纯函数做表驱动测试，覆盖：

- 前缀规范化
- 最大存活时长解析
- 过期对象筛选
- 删除批次切分

### 调度测试

使用假的 R2 bucket 对象模拟：

- 多页 list
- 部分对象未过期
- 删除按 1000 条分批
- 删除异常不中断后续批次

### 本地联调

使用 Wrangler 的 scheduled 调试能力做本地验证：

```bash
npx wrangler dev --config cloudflare/r2-cleaner/wrangler.jsonc --test-scheduled
```

然后访问：

```text
/__scheduled
```

以人工触发一次定时任务。

---

## 文档与运维要求

`README.md` 需要新增一节，说明：

- 清理 Worker 的作用
- 默认 Cron 与默认阈值
- 如何配置 `R2_CLEANUP_PREFIX`
- 如何调整执行频率
- 如何本地测试
- 如何部署

同时强调：

- 生产环境默认建议前缀与 `R2_OBJECT_PREFIX` 保持一致
- 该 Worker 面向独占 bucket 或明确受控前缀

---

## 风险与对策

### 风险 1：误删非业务对象

对策：

- 默认启用前缀限制
- README 明确不建议随意清空前缀配置

### 风险 2：大量历史对象导致单次扫描耗时变长

对策：

- 使用分页 list
- 单次删除按批切分
- 保留汇总日志，便于后续决定是否增加执行频率

### 风险 3：配置值不合法导致实际删除范围偏离预期

对策：

- 在 Worker 启动路径统一解析配置
- 对非法 `R2_CLEANUP_MAX_AGE_SECONDS` 直接抛错

---

## 最终决策

本次采用以下固定方案：

- 独立目录：`cloudflare/r2-cleaner/`
- 运行位置：Cloudflare Worker
- 调度方式：Cron Trigger
- 默认频率：每 30 分钟
- 删除依据：`uploaded`
- 默认阈值：3 小时
- 默认范围：`images` 前缀
- 并发策略：无锁，接受幂等重试

该方案以最小改动实现“分钟级可配置清理”，并保持业务服务无状态、
无竞争、易部署。
