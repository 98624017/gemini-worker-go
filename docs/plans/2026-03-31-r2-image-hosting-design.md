# R2 图片托管设计

**目标**

在现有 `output=url` 响应改写链路中，新增可选的 R2 图片托管能力。
服务需要支持通过环境变量在三种模式之间切换：

- `legacy`：保持现有图床上传链路
- `r2`：仅上传到 R2
- `r2_then_legacy`：优先上传到 R2，失败后回退到现有图床链路

同时，R2 成功上传后的返回地址必须使用自定义公网域名前缀，并且
R2 返回结果不再套现有 `/proxy/image` 代理包装。

---

## 背景

当前仓库已经具备完整的“响应中的 base64 图片上传到图床并改写为 URL”
能力，核心逻辑集中在 `main.go`：

- `handleNonStreamResponse`
- `handleStreamResponse`
- `convertInlineDataBase64ToUrlInResponse`
- `uploadImageBytesToUrl`
- `uploadToUguu`
- `uploadToKefan`

现状问题有两点：

1. 上传目的地被写死为 `uguu -> kefan`
2. 无法切换到自有可控存储，也无法显式表达回退策略

本次设计只扩展“上传去哪儿”的能力，不改动现有响应遍历、并发替换、
多图去重与 fail-open 主流程。

---

## 设计原则

1. 复用现有响应改写主链路，不重写 `output=url` 处理流程
2. 配置面最小化，避免新增多套重复的超时和 TLS 开关
3. 启动期对配置错误 fail-fast，运行期对上传失败保持 fail-open
4. 将“上传提供方选择”和“URL 是否包代理”拆成两个独立决策
5. 为后续扩展其他对象存储保留清晰边界

---

## 方案对比

### 方案 A：上传器抽象 + 三态模式分发

保留现有响应改写入口，在上传层引入统一分发入口，根据
`IMAGE_HOST_MODE` 决定调用 `legacy` 或 `R2` 上传器。

优点：

- 改动边界清晰
- 测试更容易隔离
- 以后接入新存储后无需再污染响应改写逻辑

缺点：

- 比单纯 `if/else` 多一层抽象

### 方案 B：直接在 `uploadImageBytesToUrl` 内堆叠条件分支

优点：

- 初期改动最少

缺点：

- 上传策略、回退逻辑、URL 包装规则会逐步耦合在一起
- 后续扩展其他存储时成本更高

### 方案 C：手写 R2 HTTP 签名与上传逻辑

优点：

- 可减少外部依赖

缺点：

- 实现和维护成本高
- 调试复杂，收益不成比例

### 结论

采用 **方案 A**。它能以最小风险把上传能力从“写死图床”升级为
“可配置托管策略”。

---

## 总体架构

整体链路保持不变：

1. 上游返回 JSON 或 SSE 响应
2. 服务继续做 Markdown 图片归一化、多图去重
3. 当 `output=url` 时，遍历所有 base64 图片
4. 将每张图片交给统一上传入口
5. 上传入口根据模式选择 R2 或 legacy 图床
6. 使用最终 URL 回写 `inlineData.data`

本次新增的变化只发生在第 4 到第 6 步。

### 架构边界

- `convertInlineDataBase64ToUrlInResponse`
  继续负责遍历、并发、替换
- `uploadImageBytesToUrl`
  升级为“上传策略分发入口”
- 新增 `uploadToR2`
  负责将图片写入 R2 并返回自定义公网 URL
- 新增上传结果元信息
  用于区分最终 URL 来自 `legacy` 还是 `r2`

---

## 模式设计

新增环境变量 `IMAGE_HOST_MODE`，支持以下取值：

### `legacy`

行为与当前版本一致：

- 先尝试 `uploadToUguu`
- 再尝试 `uploadToKefan`
- 返回的 URL 继续受 `PROXY_STANDARD_OUTPUT_URLS` 控制

### `r2`

仅尝试 R2：

- 成功则返回 `R2_PUBLIC_BASE_URL + objectKey`
- 失败则向上返回上传错误
- 外层保持当前 fail-open，记录日志并保留原始 base64

### `r2_then_legacy`

优先 R2，失败回退 legacy：

- 先尝试 `uploadToR2`
- 失败时记录回退日志
- 再尝试现有 `uguu -> kefan`
- 如果最终回退到 legacy，则继续沿用现有 URL 包装逻辑

### 默认值

默认值为 `legacy`，确保未配置时完全兼容当前行为。

---

## 环境变量设计

### 新增变量

- `IMAGE_HOST_MODE`
- `R2_ENDPOINT`
- `R2_BUCKET`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_PUBLIC_BASE_URL`
- `R2_OBJECT_PREFIX`

### 字段说明

#### `IMAGE_HOST_MODE`

上传策略模式。

支持值：

- `legacy`
- `r2`
- `r2_then_legacy`

默认：`legacy`

#### `R2_ENDPOINT`

R2 S3 兼容接口地址，例如：

`https://<accountid>.r2.cloudflarestorage.com`

#### `R2_BUCKET`

目标存储桶名称。

#### `R2_ACCESS_KEY_ID`

R2 访问密钥 ID。

#### `R2_SECRET_ACCESS_KEY`

R2 访问密钥 Secret。

#### `R2_PUBLIC_BASE_URL`

对外返回的自定义公网域名前缀，例如：

`https://img.example.com`

服务只负责拼接 URL，不负责验证该域名是否已完成 Cloudflare 侧绑定。

#### `R2_OBJECT_PREFIX`

对象 key 前缀，可选，默认 `images`。

---

## 配置校验规则

### `legacy`

当 `IMAGE_HOST_MODE=legacy` 时：

- 不要求 R2 配置
- R2 变量全部忽略

### `r2` / `r2_then_legacy`

当模式为 `r2` 或 `r2_then_legacy` 时，以下字段必须完整：

- `R2_ENDPOINT`
- `R2_BUCKET`
- `R2_ACCESS_KEY_ID`
- `R2_SECRET_ACCESS_KEY`
- `R2_PUBLIC_BASE_URL`

如果缺失、为空或 URL 非法：

- 直接启动失败
- 输出明确日志

这样可以把配置错误前置到部署阶段，而不是首个线上请求才暴露。

---

## R2 对象 Key 规则

对象 key 采用时间目录 + 随机串，默认格式如下：

```text
images/YYYY/MM/DD/<timestamp>-<rand>.<ext>
```

示例：

```text
images/2026/03/31/1743393005123-a1b2c3d4.png
```

说明：

- `images` 来自 `R2_OBJECT_PREFIX`
- 日期目录方便后续生命周期规则和人工排查
- `<timestamp>-<rand>` 能降低冲突概率
- 扩展名由现有 `mimeType -> extension` 逻辑推断

本次不采用内容哈希命名，原因如下：

1. 当前没有跨请求图片去重的明确需求
2. 哈希命名会暴露“相同图片得到相同 URL”的可观察性
3. 时间戳 + 随机串更简单，也足够满足当前需求

---

## URL 返回与代理包装规则

### legacy 上传结果

保持现有逻辑：

- `PROXY_STANDARD_OUTPUT_URLS=1` 时可走 `/proxy/image`
- `PROXY_STANDARD_OUTPUT_URLS=0` 时直出原始图床 URL

### R2 上传结果

R2 返回结果 **永远直出**，不走 `/proxy/image` 包装。

原因：

1. R2 已经是自定义图片域名
2. 再包一层代理收益有限，反而增加一跳
3. 需求已明确要求“仅 legacy 走包装”

### `r2_then_legacy`

根据最终成功提供方决定：

- 最终来自 R2：直出
- 最终来自 legacy：沿用现有包装规则

因此，上传结果除了 URL 之外，还需要包含“最终 provider”信息。

---

## 运行期错误处理

### 启动期

启动期采用 fail-fast：

- 模式非法：启动失败
- R2 必填配置缺失：启动失败
- `R2_PUBLIC_BASE_URL` 非法：启动失败

### 请求处理期

请求处理期保持当前 fail-open 风格：

- 单张图上传失败时，返回错误给转换层
- `convertInlineDataBase64ToUrlInResponse` 收到错误后：
  - 记录日志
  - 不返回 5xx
  - 保留原始 base64 响应

### `r2_then_legacy`

当 R2 失败且 legacy 成功时：

- 请求对外仍视为成功
- 记录“R2 失败但已回退 legacy”的结构化日志

这样既不影响主链路可用性，也能保留观测信息。

---

## 日志与可观测性

建议至少输出以下维度：

- `mode`
- `provider`
- `mimeType`
- `objectKey`（R2 成功时）
- `error`

关键场景：

1. `r2` 模式上传失败
2. `r2_then_legacy` 模式发生回退
3. R2 成功上传并生成 URL

日志重点是区分三类情况：

- 本来就在走 legacy
- R2 正常成功
- R2 失败后已回退 legacy

---

## 测试策略

### 配置解析测试

覆盖以下场景：

- `IMAGE_HOST_MODE` 默认值为 `legacy`
- 非法模式触发明确错误
- `r2` / `r2_then_legacy` 缺失 R2 必填项时报错
- `R2_PUBLIC_BASE_URL` 非法时报错

### 上传分发测试

覆盖以下分支：

- `legacy` 只调用旧链路
- `r2` 只调用 R2
- `r2_then_legacy` 中 R2 成功时不触发 legacy
- `r2_then_legacy` 中 R2 失败时回退 legacy

### URL 包装测试

覆盖以下语义：

- legacy 继续遵守 `PROXY_STANDARD_OUTPUT_URLS`
- R2 始终直出
- `r2_then_legacy` 根据最终 provider 决定是否包装

### 响应改写回归测试

必须确保现有行为不被破坏：

- `output=url` 仍能将 base64 改写为 URL
- 多图场景仍只保留最大图片
- 上传失败时继续 fail-open，保留 base64

### 对象 Key 测试

验证：

- key 前缀正确
- 日期目录格式正确
- 扩展名从 `mimeType` 推断正确

---

## 兼容性结论

该设计满足以下兼容要求：

1. 未配置时默认 `legacy`，现有行为不变
2. 只在用户显式开启 `r2` 或 `r2_then_legacy` 时启用 R2
3. legacy 结果仍保留现有 `/proxy/image` 包装能力
4. R2 结果固定直出，不引入新的额外代理层
5. 上传失败不打断主请求，保持当前同步接口的容错特性

---

## 实施边界

本次设计只覆盖同步 Go 服务根目录实现：

- 不修改 `async-gateway/`
- 不引入额外异步上传流程
- 不做跨请求图片去重
- 不新增图片生命周期清理机制

后续如果需要：

- R2 生命周期规则
- 内容哈希去重
- 多提供方熔断统计

可以在本设计的上传器边界上继续扩展。
