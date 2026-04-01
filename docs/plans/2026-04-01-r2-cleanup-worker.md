# R2 Cleanup Worker Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为当前仓库使用的 R2 bucket 增加一个独立的 Cloudflare Worker 定时清理器，默认每 30 分钟删除 `R2_OBJECT_PREFIX` 对应前缀下上传时间超过 3 小时的对象，并允许频率和阈值可配置。

**Architecture:** 在仓库内新增 `cloudflare/r2-cleaner/` 子项目，使用模块化 JavaScript Worker 与 Wrangler 配置承载 Cron 与 R2 binding。实现上把“配置解析”“过期判定与批次切分”“scheduled 主循环”拆成可单测的纯函数和一个薄的调度入口，避免把 Cloudflare 运行时细节散落到业务逻辑中。

**Tech Stack:** Cloudflare Workers、Wrangler、JavaScript ES Modules、Node.js 内置 `node:test`、现有仓库 `README.md` 文档体系。

---

### Task 1: 搭好 Worker 骨架与默认配置

**Files:**
- Create: `cloudflare/r2-cleaner/package.json`
- Create: `cloudflare/r2-cleaner/wrangler.jsonc`
- Create: `cloudflare/r2-cleaner/worker.js`
- Create: `cloudflare/r2-cleaner/worker.test.js`

**Step 1: Write the failing test**

在 `cloudflare/r2-cleaner/worker.test.js` 中先写配置默认值测试，明确后续实现边界：

```js
import test from "node:test";
import assert from "node:assert/strict";
import { getCleanupConfig } from "./worker.js";

test("getCleanupConfig uses default prefix and max age", () => {
  const config = getCleanupConfig({});
  assert.equal(config.prefix, "images");
  assert.equal(config.maxAgeSeconds, 10800);
});
```

**Step 2: Run test to verify it fails**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
FAIL
Cannot find module './worker.js'
```

**Step 3: Write minimal implementation**

1. 新建 `package.json`，使用 ESM：

```json
{
  "name": "r2-cleanup-worker",
  "private": true,
  "type": "module",
  "scripts": {
    "test": "node --test worker.test.js"
  }
}
```

2. 新建 `wrangler.jsonc`，先放默认 Cron 与 R2 绑定占位：

```json
{
  "name": "banana-r2-cleaner",
  "main": "worker.js",
  "compatibility_date": "2026-04-01",
  "triggers": {
    "crons": ["*/30 * * * *"]
  },
  "r2_buckets": [
    {
      "binding": "R2_BUCKET",
      "bucket_name": "TODO_REPLACE_BUCKET_NAME"
    }
  ]
}
```

3. 新建 `worker.js`，只导出最小配置解析：

```js
export function getCleanupConfig(env) {
  const prefix = String(env.R2_CLEANUP_PREFIX ?? "images").trim().replace(/^\/+|\/+$/g, "");
  const rawMaxAge = String(env.R2_CLEANUP_MAX_AGE_SECONDS ?? "10800").trim();
  const maxAgeSeconds = Number.parseInt(rawMaxAge, 10);
  if (!Number.isFinite(maxAgeSeconds) || maxAgeSeconds <= 0) {
    throw new Error("R2_CLEANUP_MAX_AGE_SECONDS must be a positive integer");
  }
  return { prefix, maxAgeSeconds };
}

export default {
  async scheduled() {},
};
```

**Step 4: Run test to verify it passes**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/package.json cloudflare/r2-cleaner/wrangler.jsonc cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js
git commit -m "feat: scaffold r2 cleanup worker"
```

### Task 2: 固化配置解析与边界规则

**Files:**
- Modify: `cloudflare/r2-cleaner/worker.js`
- Modify: `cloudflare/r2-cleaner/worker.test.js`

**Step 1: Write the failing tests**

补齐配置边界测试，覆盖前缀规范化与非法阈值：

```js
test("getCleanupConfig trims slashes from prefix", () => {
  const config = getCleanupConfig({ R2_CLEANUP_PREFIX: "/images/" });
  assert.equal(config.prefix, "images");
});

test("getCleanupConfig allows empty prefix for full bucket scan", () => {
  const config = getCleanupConfig({ R2_CLEANUP_PREFIX: " / " });
  assert.equal(config.prefix, "");
});

test("getCleanupConfig rejects non-positive max age", () => {
  assert.throws(
    () => getCleanupConfig({ R2_CLEANUP_MAX_AGE_SECONDS: "0" }),
    /positive integer/,
  );
});
```

**Step 2: Run test to verify it fails**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
FAIL
AssertionError
```

**Step 3: Write minimal implementation**

在 `worker.js` 中补足纯函数，确保规则集中：

```js
export function normalizePrefix(value) {
  return String(value ?? "").trim().replace(/^\/+|\/+$/g, "");
}

export function parsePositiveInt(value, fieldName, defaultValue) {
  const raw = String(value ?? defaultValue).trim();
  const parsed = Number.parseInt(raw, 10);
  if (!Number.isFinite(parsed) || parsed <= 0) {
    throw new Error(`${fieldName} must be a positive integer`);
  }
  return parsed;
}

export function getCleanupConfig(env) {
  return {
    prefix: normalizePrefix(env.R2_CLEANUP_PREFIX ?? "images"),
    maxAgeSeconds: parsePositiveInt(
      env.R2_CLEANUP_MAX_AGE_SECONDS,
      "R2_CLEANUP_MAX_AGE_SECONDS",
      "10800",
    ),
  };
}
```

**Step 4: Run test to verify it passes**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js
git commit -m "feat: validate r2 cleanup worker config"
```

### Task 3: 提取过期判定与批量删除辅助函数

**Files:**
- Modify: `cloudflare/r2-cleaner/worker.js`
- Modify: `cloudflare/r2-cleaner/worker.test.js`

**Step 1: Write the failing tests**

先写纯函数测试，锁定删除判定与 1000 条分批行为：

```js
import { collectExpiredKeys, chunkKeys } from "./worker.js";

test("collectExpiredKeys returns only keys older than cutoff", () => {
  const cutoff = new Date("2026-04-01T09:00:00Z");
  const objects = [
    { key: "images/a.png", uploaded: new Date("2026-04-01T08:00:00Z") },
    { key: "images/b.png", uploaded: new Date("2026-04-01T10:00:00Z") },
  ];
  assert.deepEqual(collectExpiredKeys(objects, cutoff), ["images/a.png"]);
});

test("chunkKeys splits keys into batches of 1000", () => {
  const keys = Array.from({ length: 1001 }, (_, index) => `images/${index}.png`);
  const chunks = chunkKeys(keys, 1000);
  assert.equal(chunks.length, 2);
  assert.equal(chunks[0].length, 1000);
  assert.equal(chunks[1].length, 1);
});
```

**Step 2: Run test to verify it fails**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
FAIL
collectExpiredKeys is not exported
```

**Step 3: Write minimal implementation**

在 `worker.js` 中新增辅助函数：

```js
export function collectExpiredKeys(objects, cutoff) {
  return objects
    .filter((object) => object.uploaded instanceof Date && object.uploaded <= cutoff)
    .map((object) => object.key);
}

export function chunkKeys(keys, batchSize = 1000) {
  const chunks = [];
  for (let index = 0; index < keys.length; index += batchSize) {
    chunks.push(keys.slice(index, index + batchSize));
  }
  return chunks;
}
```

**Step 4: Run test to verify it passes**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js
git commit -m "feat: add cleanup selection helpers"
```

### Task 4: 实现分页扫描与删除主循环

**Files:**
- Modify: `cloudflare/r2-cleaner/worker.js`
- Modify: `cloudflare/r2-cleaner/worker.test.js`

**Step 1: Write the failing tests**

用假 bucket 写出 `scheduled()` 的核心行为测试，至少覆盖：

```js
test("scheduled lists by prefix and deletes only expired keys across pages", async () => {
  const deletedBatches = [];
  const bucket = {
    async list(options) {
      if (!options.cursor) {
        return {
          objects: [
            { key: "images/a.png", uploaded: new Date("2026-04-01T00:00:00Z") },
            { key: "images/b.png", uploaded: new Date("2026-04-01T11:30:00Z") },
          ],
          truncated: true,
          cursor: "page-2",
        };
      }
      return {
        objects: [
          { key: "images/c.png", uploaded: new Date("2026-04-01T01:00:00Z") },
        ],
        truncated: false,
      };
    },
    async delete(keys) {
      deletedBatches.push(keys);
    },
  };

  await runCleanup(
    { R2_BUCKET: bucket, R2_CLEANUP_PREFIX: "images", R2_CLEANUP_MAX_AGE_SECONDS: "10800" },
    { now: new Date("2026-04-01T12:00:00Z") },
  );

  assert.deepEqual(deletedBatches, [["images/a.png", "images/c.png"]]);
});
```

再加一个删除异常不中断的测试：

```js
test("runCleanup keeps counting delete errors and continues", async () => {
  // 第一批 delete 抛错，第二批成功，最终返回 deleteErrorCount = 1
});
```

**Step 2: Run test to verify it fails**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
FAIL
runCleanup is not exported
```

**Step 3: Write minimal implementation**

在 `worker.js` 中实现主流程，并让 `scheduled()` 调用它：

```js
export async function runCleanup(env, options = {}) {
  const now = options.now ?? new Date();
  const log = options.log ?? console;
  const bucket = env.R2_BUCKET;
  if (!bucket || typeof bucket.list !== "function" || typeof bucket.delete !== "function") {
    throw new Error("R2_BUCKET binding is required");
  }

  const { prefix, maxAgeSeconds } = getCleanupConfig(env);
  const cutoff = new Date(now.getTime() - maxAgeSeconds * 1000);

  let cursor;
  let listedCount = 0;
  let expiredCount = 0;
  let deletedCount = 0;
  let deleteErrorCount = 0;
  let pageCount = 0;

  do {
    const page = await bucket.list({ prefix, cursor });
    pageCount += 1;
    const objects = Array.isArray(page.objects) ? page.objects : [];
    listedCount += objects.length;
    const expiredKeys = collectExpiredKeys(objects, cutoff);
    expiredCount += expiredKeys.length;

    for (const batch of chunkKeys(expiredKeys, 1000)) {
      try {
        await bucket.delete(batch);
        deletedCount += batch.length;
      } catch (error) {
        deleteErrorCount += 1;
        log.error("r2 cleanup delete batch failed", {
          prefix,
          batchSize: batch.length,
          firstKey: batch[0],
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }

    cursor = page.truncated ? page.cursor : undefined;
  } while (cursor);

  const summary = {
    prefix,
    maxAgeSeconds,
    cutoff: cutoff.toISOString(),
    listedCount,
    expiredCount,
    deletedCount,
    deleteErrorCount,
    pageCount,
  };
  log.log("r2 cleanup completed", summary);
  return summary;
}

export default {
  async scheduled(controller, env, ctx) {
    ctx.waitUntil(runCleanup(env, { log: console, cron: controller?.cron }));
  },
};
```

**Step 4: Run test to verify it passes**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js
git commit -m "feat: implement scheduled r2 cleanup flow"
```

### Task 5: 补全 Worker 调度与本地验证入口

**Files:**
- Modify: `cloudflare/r2-cleaner/wrangler.jsonc`
- Modify: `cloudflare/r2-cleaner/package.json`
- Modify: `cloudflare/r2-cleaner/worker.test.js`

**Step 1: Write the failing test**

增加一个轻量测试，确保 `scheduled()` 通过 `ctx.waitUntil()` 包装 `runCleanup()`：

```js
test("scheduled hands cleanup promise to waitUntil", async () => {
  let capturedPromise;
  const ctx = {
    waitUntil(promise) {
      capturedPromise = promise;
    },
  };
  await worker.scheduled({ cron: "*/30 * * * *" }, { R2_BUCKET: fakeBucket }, ctx);
  assert.ok(capturedPromise);
  await capturedPromise;
});
```

**Step 2: Run test to verify it fails**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
FAIL
capturedPromise is undefined
```

**Step 3: Write minimal implementation**

1. 调整 `worker.js` 的默认导出，确保 `scheduled()` 把 Promise 交给 `ctx.waitUntil`
2. 在 `package.json` 中增加本地命令：

```json
{
  "scripts": {
    "test": "node --test worker.test.js",
    "dev": "wrangler dev --config wrangler.jsonc --test-scheduled"
  }
}
```

3. 在 `wrangler.jsonc` 中补充注释友好的变量占位：

```json
{
  "vars": {
    "R2_CLEANUP_PREFIX": "images",
    "R2_CLEANUP_MAX_AGE_SECONDS": "10800"
  }
}
```

**Step 4: Run test to verify it passes**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/package.json cloudflare/r2-cleaner/wrangler.jsonc cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js
git commit -m "chore: finalize r2 cleanup worker runtime config"
```

### Task 6: 更新 README 与部署说明

**Files:**
- Modify: `README.md`

**Step 1: Write the failing doc expectation**

先整理要补充的文档要点，至少包含：

- 清理 Worker 的目录位置
- 默认 Cron：`*/30 * * * *`
- 默认阈值：`10800`
- `R2_CLEANUP_PREFIX` 和 `R2_CLEANUP_MAX_AGE_SECONDS`
- `npx wrangler dev --config cloudflare/r2-cleaner/wrangler.jsonc --test-scheduled`
- `npx wrangler deploy --config cloudflare/r2-cleaner/wrangler.jsonc`

把这些点作为核对清单，确保 README 修改后逐项可见。

**Step 2: Run a quick search to verify the section is missing**

Run:

```bash
rg -n "R2_CLEANUP_PREFIX|r2-cleaner|test-scheduled" README.md
```

Expected:

```text
no matches found
```

**Step 3: Write minimal implementation**

在 `README.md` 新增一个独立章节，例如：

```md
### R2 定时清理 Worker

仓库内提供独立的 Cloudflare Worker 定时清理器，目录：

`cloudflare/r2-cleaner/`

默认配置：

- Cron：`*/30 * * * *`
- `R2_CLEANUP_PREFIX=images`
- `R2_CLEANUP_MAX_AGE_SECONDS=10800`

本地调试：

```bash
npx wrangler dev --config cloudflare/r2-cleaner/wrangler.jsonc --test-scheduled
```

部署：

```bash
npx wrangler deploy --config cloudflare/r2-cleaner/wrangler.jsonc
```
```

并补一段说明：生产建议让 `R2_CLEANUP_PREFIX` 与 Go 服务的
`R2_OBJECT_PREFIX` 保持一致。

**Step 4: Run search to verify the section exists**

Run:

```bash
rg -n "R2_CLEANUP_PREFIX|r2-cleaner|test-scheduled" README.md
```

Expected:

```text
README.md:<line>:### R2 定时清理 Worker
README.md:<line>:- `R2_CLEANUP_PREFIX=images`
```

**Step 5: Commit**

```bash
git add README.md
git commit -m "docs: add r2 cleanup worker guide"
```

### Task 7: 做一次本地验证清单收口

**Files:**
- Modify: `cloudflare/r2-cleaner/worker.js`
- Modify: `cloudflare/r2-cleaner/worker.test.js`
- Modify: `README.md`

**Step 1: Run the focused automated tests**

Run:

```bash
node --test cloudflare/r2-cleaner/worker.test.js
```

Expected:

```text
ok
```

**Step 2: Run a formatting / syntax sanity check**

Run:

```bash
node --check cloudflare/r2-cleaner/worker.js
```

Expected:

```text
[no output]
```

**Step 3: Run a local scheduled smoke test**

Run:

```bash
npx wrangler dev --config cloudflare/r2-cleaner/wrangler.jsonc --test-scheduled
```

Expected:

```text
Ready on http://127.0.0.1:8787
```

然后访问：

```text
http://127.0.0.1:8787/__scheduled
```

预期：

- Worker 打出 `r2 cleanup completed`
- 汇总日志中包含 `prefix`、`cutoff`、`deletedCount`

**Step 4: 根据验证结果做最后微调**

若 smoke test 暴露问题，只修最小必要改动，例如：

- `ctx.waitUntil` 未生效
- `list()` 分页 cursor 处理错误
- README 的命令路径与实际不一致

修完后重复前 3 步，直到全部通过。

**Step 5: Commit**

```bash
git add cloudflare/r2-cleaner/worker.js cloudflare/r2-cleaner/worker.test.js README.md
git commit -m "test: verify r2 cleanup worker locally"
```
