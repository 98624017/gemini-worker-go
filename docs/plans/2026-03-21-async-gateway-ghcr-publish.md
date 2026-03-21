# Async Gateway GHCR Publish Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为 `async-gateway` 新增 GitHub Actions 到 GHCR 的自动镜像构建与推送流程，并补齐使用文档。

**Architecture:** 在仓库根目录新增独立的发布 workflow，监听 `main` 与 `v*` tag。workflow 直接以 `async-gateway/` 为构建上下文，使用 Docker 官方 actions 生成标签、登录 GHCR、构建并推送镜像；README 同步说明触发时机、标签规则与拉取方式。

**Tech Stack:** GitHub Actions, Docker Buildx, GHCR, Markdown

---

### Task 1: 增加 GHCR 发布工作流

**Files:**
- Create: `.github/workflows/async-gateway-ghcr.yml`
- Modify: `async-gateway/Dockerfile`（仅当实现时发现必须调整平台构建逻辑）

**Step 1: 明确标签与权限策略**

- `push main` 推送 `main` 与 `sha-<shortsha>`
- `push tag v*` 推送 `vX.Y.Z`、`X.Y.Z`、`X.Y`、`X`、`latest`
- `workflow_dispatch` 支持手动触发
- job 权限最小化为 `contents: read` 与 `packages: write`

**Step 2: 编写 workflow**

- 使用 `actions/checkout@v4`
- 使用 `docker/setup-buildx-action@v3`
- 使用 `docker/login-action@v3`
- 使用 `docker/metadata-action@v5`
- 使用 `docker/build-push-action@v6`
- 固定镜像名为 `ghcr.io/<owner>/banana-async-gateway`
- 构建上下文固定为 `async-gateway/`
- 平台先固定 `linux/amd64`
- 开启 `gha` cache

**Step 3: 自检 workflow 逻辑**

检查：
- `main` 分支不会生成 `latest`
- tag 发布会生成 `latest`
- 镜像名 owner 已转小写，避免 GHCR 名称非法
- 路径过滤仅覆盖 `async-gateway/**` 与 workflow 文件自身

### Task 2: 更新使用文档

**Files:**
- Modify: `async-gateway/README.md`

**Step 1: 补充 GHCR 发布说明**

新增内容：
- workflow 文件名
- 触发条件
- 标签规则
- `GITHUB_TOKEN` 权限要求

**Step 2: 补充拉取示例**

新增示例：
- `docker pull ghcr.io/<owner>/banana-async-gateway:main`
- `docker pull ghcr.io/<owner>/banana-async-gateway:v1.2.3`

**Step 3: 标注当前镜像架构**

- 说明当前镜像与现有 `Dockerfile` 一致，默认发布 `linux/amd64`

### Task 3: 验证与收尾

**Files:**
- Verify: `.github/workflows/async-gateway-ghcr.yml`
- Verify: `async-gateway/README.md`

**Step 1: 校验 YAML 可读性**

Run: `sed -n '1,260p' .github/workflows/async-gateway-ghcr.yml`

Expected: 结构完整，触发器、权限、metadata tags、build-push 参数均正确

**Step 2: 运行仓库相关测试**

Run: `go test ./... -count=1`

Expected: `async-gateway` 既有测试继续通过

**Step 3: 检查最终 diff**

Run: `git diff -- .github/workflows/async-gateway-ghcr.yml async-gateway/README.md docs/plans/2026-03-21-async-gateway-ghcr-publish.md`

Expected: 仅包含计划文档、发布 workflow、README 说明三类变更
