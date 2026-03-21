# Async Gateway Zeabur Migration Job Design

## 背景

当前 `async-gateway` 镜像只包含 `banana-async-gateway` 运行二进制，不包含
`banana-async-migrate` 和 `migrations/`。这导致 Zeabur 首次部署时，如果目标
PostgreSQL 还没有 `tasks`、`task_payloads` 表，服务启动会在恢复扫描阶段直接失败。

现象已经在线上复现：

- Zeabur 能成功拉取 `ghcr.io/98624017/banana-async-gateway:main`
- 服务启动时执行恢复扫描
- 数据库返回 `relation "tasks" does not exist`
- 容器不断重启

## 目标

让 Zeabur 首次上线支持“独立 migration 任务”模式：

1. 使用与正式服务相同的镜像
2. 临时创建一个 migration 服务或任务
3. 覆盖启动命令为 `banana-async-migrate up`
4. migration 成功后删除临时服务
5. 再部署正常版 `banana-async-gateway`

## 备选方案

### 方案 A：同一镜像双用途

做法：

- 在现有 `async-gateway` 运行镜像里同时打包：
  - `banana-async-gateway`
  - `banana-async-migrate`
  - `migrations/`
- 正常服务继续使用默认 `ENTRYPOINT ["banana-async-gateway"]`
- Zeabur migration 服务通过覆盖启动命令执行 `banana-async-migrate up`

优点：

- 镜像来源单一，发布链路最简单
- Zeabur 侧只需要“一个正式服务 + 一个临时任务”
- 不需要维护第二套镜像命名与推送逻辑

缺点：

- 运行镜像体积会略增
- 需要保证 `migrations/` 在镜像中的路径与 migrate 命令一致

### 方案 B：单独维护 migration 镜像

做法：

- 为 migration 单独构建一个镜像
- Zeabur 首次上线时先跑 migration 镜像
- 正式服务继续使用运行镜像

优点：

- 职责隔离更彻底
- 正式服务镜像更“纯”

缺点：

- GHCR 需要维护第二个镜像名或第二套 tag 规则
- 文档、发布、回滚都更复杂
- 对当前仓库是过度设计

### 方案 C：服务启动时自动 migrate

做法：

- 每次容器启动先 `migrate up`
- 再执行 `banana-async-gateway`

优点：

- 首次部署最省心

缺点：

- 多副本启动时更容易把 migration 和应用生命周期耦合在一起
- 失败时定位更差
- 不符合当前已经确认的“独立 migration 任务”方向

## 推荐方案

选择 **方案 A：同一镜像双用途**。

原因：

- 满足 Zeabur 的首次部署场景
- 最少修改现有 GHCR 发布流程
- 不引入新的镜像仓库、额外 tag 策略或启动链复杂度

## 设计

### 镜像内容

调整 `async-gateway/Dockerfile`：

- 构建阶段额外编译 `./cmd/banana-async-migrate`
- 运行阶段复制两个二进制：
  - `/usr/local/bin/banana-async-gateway`
  - `/usr/local/bin/banana-async-migrate`
- 运行阶段复制 migration 文件到固定目录：
  - `/app/migrations`

`banana-async-migrate` 当前使用 `file://migrations`，因此运行目录必须保持在
`/app`，这样在容器里直接执行 `banana-async-migrate up` 就能找到
`/app/migrations`。

### Zeabur 操作流

首次部署：

1. 准备 PostgreSQL 和正式服务共用的环境变量
2. 新建临时 migration 服务
3. 镜像使用 `ghcr.io/98624017/banana-async-gateway:<tag>`
4. 启动命令覆盖为：
   `banana-async-migrate up`
5. 日志看到 `migrate up complete` 或 `no change`
6. 删除临时 migration 服务
7. 部署正常服务，默认命令运行 `banana-async-gateway`

后续版本：

- 如果没有新的 migration，直接更新正式服务
- 如果新增 migration，先跑一次临时 migration 服务，再更新正式服务

### 文档更新

`async-gateway/README.md` 需要新增：

- Zeabur 的独立 migration 任务流程
- 首次部署步骤
- 成功日志特征
- 注意事项：
  - migration 服务不用暴露端口
  - migration 和正式服务必须使用同一个 `POSTGRES_DSN`
  - 临时服务成功后即可删除

## 错误处理

- `POSTGRES_DSN` 错误：
  migration 日志会直接失败，正式服务不会继续启动
- `migrations/` 未打包进镜像：
  会在 `banana-async-migrate up` 阶段报找不到文件
- 数据库已是最新：
  `banana-async-migrate` 返回 `no change`，视为成功

## 测试与验证

由于本次改动主要是镜像打包与部署文档，不引入 Go 业务逻辑变更，验证重点应放在：

1. `go test ./... -count=1 -timeout 60s`
2. 检查 Dockerfile 确认两个二进制和 `migrations/` 都被复制到运行镜像
3. 如果本地 Docker 可用，再执行一次镜像构建并确认：
   - `banana-async-migrate version`
   - `banana-async-gateway` 仍可正常作为默认入口启动

## 用户操作说明

设计落地后，Zeabur 的推荐操作将是：

1. 先部署临时 migration 服务并运行成功
2. 删除临时 migration 服务
3. 再部署正式服务

这是一次性初始化步骤，不是长期双服务运行模式。
