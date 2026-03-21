# Async Gateway Zeabur Migration Job Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让 `async-gateway` 的 GHCR 镜像同时支持 Zeabur 临时 migration 任务和正式服务启动。

**Architecture:** 复用同一个运行镜像，默认入口仍是 `banana-async-gateway`，但镜像内额外打包 `banana-async-migrate` 与 `migrations/`，从而允许 Zeabur 通过覆盖启动命令执行一次性 migration。README 同步补齐首次上线与后续升级的操作指南。

**Tech Stack:** Docker multi-stage build, Go 1.25, GitHub Actions GHCR, Markdown

---

### Task 1: 更新 Docker 打包内容

**Files:**
- Modify: `async-gateway/Dockerfile`

**Step 1: 写出失败前提的校验思路**

- 明确镜像当前缺少：
  - `banana-async-migrate`
  - `migrations/`
- 记录预期：最终运行镜像内应同时存在两个二进制和 `/app/migrations`

**Step 2: 实现最小 Dockerfile 改动**

- 在构建阶段额外执行：
  `go build ... ./cmd/banana-async-migrate`
- 在运行阶段复制：
  - `/out/banana-async-gateway`
  - `/out/banana-async-migrate`
  - `/src/migrations`

**Step 3: 检查默认入口不变**

- 保持：
  `ENTRYPOINT ["banana-async-gateway"]`

**Step 4: 验证 Dockerfile**

Run: `sed -n '1,220p' async-gateway/Dockerfile`

Expected:
- 同时构建两个二进制
- 运行镜像包含 `migrations/`
- 默认入口仍是 `banana-async-gateway`

### Task 2: 更新部署文档

**Files:**
- Modify: `async-gateway/README.md`

**Step 1: 增加 Zeabur migration 任务说明**

补充：

- 临时 migration 服务用途
- 启动命令 `banana-async-migrate up`
- 成功日志关键字

**Step 2: 增加首次上线步骤**

要求文档明确：

1. 创建临时 migration 服务
2. 使用和正式服务相同的环境变量
3. migration 成功后删除临时服务
4. 再部署正式服务

**Step 3: 增加升级场景说明**

- 无 schema 变更时直接升级正式服务
- 有 migration 时先跑临时 migration，再升级正式服务

**Step 4: 验证 README**

Run: `sed -n '150,280p' async-gateway/README.md`

Expected:
- 包含 GHCR 镜像说明
- 包含 Zeabur migration 任务流程
- 包含日志成功判定标准

### Task 3: 全量验证

**Files:**
- Verify: `async-gateway/Dockerfile`
- Verify: `async-gateway/README.md`

**Step 1: 运行 Go 测试**

Run: `go test ./... -count=1 -timeout 60s`

Expected: PASS

**Step 2: 检查变更格式**

Run: `git diff --check -- async-gateway/Dockerfile async-gateway/README.md docs/plans/2026-03-21-async-gateway-zeabur-migration-job-design.md docs/plans/2026-03-21-async-gateway-zeabur-migration-job.md`

Expected: 无空白或 patch 格式问题

**Step 3: 如果本地 Docker 可用，补一轮镜像验证**

Run: `docker build -t banana-async-gateway:test ./async-gateway`

Expected: 构建成功

后续可选验证：

- `docker run --rm banana-async-gateway:test banana-async-migrate version`
- 预期打印 migration 版本信息或 `version=none dirty=false`

**Step 4: 提交**

```bash
git add async-gateway/Dockerfile async-gateway/README.md docs/plans/2026-03-21-async-gateway-zeabur-migration-job-design.md docs/plans/2026-03-21-async-gateway-zeabur-migration-job.md
git commit -m "feat: support async-gateway zeabur migration job"
```
