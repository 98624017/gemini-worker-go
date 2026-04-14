# Rust Sync Proxy Parity Design

**目标**

把 `rust-sync-proxy` 从“主链路基本可用”补齐到与 `/go-implementation` 的关键功能等价，重点覆盖：

- `admin` 路由、日志与统计
- 特殊上游 Markdown 图片归一化
- 请求侧 `inlineData` URL 的缓存、后台桥接和相关配置

**设计**

Rust 版继续保持现有模块边界，不回退到单文件大实现。新增能力按三层落地：

1. `http/router.rs`
   负责路由编排、共享状态初始化、admin 路由接入，以及请求生命周期内的日志/统计采集。
2. `admin.rs`
   扩展为完整的 admin 数据模型、Basic Auth、环形日志缓冲和 API 输出层，保留现有 JSON 脱敏逻辑。
3. `cache.rs` 与 `request_rewrite.rs`
   把请求侧图片抓取抽象成“缓存 + 后台桥接 + 直接抓取”的统一服务，避免把复杂逻辑塞进 handler。

**行为原则**

- 配置项命名与 Go 保持一致，减少迁移成本
- `output=url` 与特殊上游图片归一化遵循 Go 的 fail-open 语义
- 后台桥接超时时返回可重试错误，不吞掉状态
- 优先补齐行为等价，再考虑进一步性能优化

**测试策略**

- 先补失败测试，再补实现
- 新增 Rust 集成测试覆盖 `admin`、Markdown 图片归一化、缓存命中与后台桥接
- 最后扩展 Go/Rust 对照脚本
