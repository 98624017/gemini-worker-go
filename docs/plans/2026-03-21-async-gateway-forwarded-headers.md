# Async Gateway Forwarded Headers Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 让 `async-gateway` 保留并转发可信上游代理已经写好的真实来源相关请求头给 `NewAPI`。

**Architecture:** 在 `submit_handler` 的固定头白名单中新增代理相关头，并在 `forwarder` 现有复制逻辑下继续原样透传，不新增任何自动补头或客户端 IP 推导逻辑。通过 `submit_handler` 和 `forwarder` 两层测试验证保存与转发行为。

**Tech Stack:** Go 1.25, net/http, testing, httptest

---

### Task 1: 为 submit 阶段补头部保存测试

**Files:**
- Modify: `async-gateway/internal/httpapi/submit_handler_test.go`

**Step 1: 写失败测试**

新增一个最小测试，验证下面这些头会被保存到 `repo.createdPayload.ForwardHeaders`：

- `X-Forwarded-For`
- `X-Real-IP`
- `X-Forwarded-Proto`
- `Forwarded`

**Step 2: 运行单测确认失败**

Run: `go test ./internal/httpapi -run TestSubmitAcceptedPreservesTrustedForwardHeaders -count=1 -timeout 60s`

Expected: FAIL，因为当前白名单不包含这些头

### Task 2: 为 forwarder 阶段补透传测试

**Files:**
- Modify: `async-gateway/internal/worker/forwarder_test.go`

**Step 1: 写失败测试**

新增一个最小测试，验证 `ForwardHeaders` 中的代理头会被实际带到上游请求：

- `X-Forwarded-For`
- `X-Real-IP`
- `X-Forwarded-Proto`
- `Forwarded`

**Step 2: 运行单测确认失败**

Run: `go test ./internal/worker -run TestForwarderPreservesTrustedForwardHeaders -count=1 -timeout 60s`

Expected: FAIL，因为当前测试夹具里未覆盖这些头

### Task 3: 实现最小代码改动

**Files:**
- Modify: `async-gateway/internal/httpapi/submit_handler.go`

**Step 1: 扩展固定白名单**

在 `extractForwardHeaders` 中新增：

- `X-Forwarded-For`
- `X-Real-IP`
- `X-Forwarded-Proto`
- `Forwarded`

**Step 2: 保持复制逻辑不变**

不修改 `copyForwardHeaders` 的敏感头拦截逻辑。

### Task 4: 跑回归

**Files:**
- Verify: `async-gateway/internal/httpapi/submit_handler.go`
- Verify: `async-gateway/internal/httpapi/submit_handler_test.go`
- Verify: `async-gateway/internal/worker/forwarder_test.go`

**Step 1: 跑针对性测试**

Run: `go test ./internal/httpapi -run TestSubmitAcceptedPreservesTrustedForwardHeaders -count=1 -timeout 60s`

Run: `go test ./internal/worker -run TestForwarderPreservesTrustedForwardHeaders -count=1 -timeout 60s`

Expected: PASS

**Step 2: 跑相关包测试**

Run: `go test ./internal/httpapi ./internal/worker -count=1 -timeout 60s`

Expected: PASS

**Step 3: 跑全量测试**

Run: `go test ./... -count=1 -timeout 60s`

Expected: PASS

**Step 4: 提交**

```bash
git add async-gateway/internal/httpapi/submit_handler.go async-gateway/internal/httpapi/submit_handler_test.go async-gateway/internal/worker/forwarder_test.go docs/plans/2026-03-21-async-gateway-forwarded-headers-design.md docs/plans/2026-03-21-async-gateway-forwarded-headers.md
git commit -m "feat: preserve trusted forwarded headers"
```
