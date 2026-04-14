# Task Dashboard Worker 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 创建一个 Cloudflare Worker，托管 React 前端 + 代理 async-gateway API + KV 缓存已完成任务，让用户通过网页输入 API key 查看最近 3 天的生图任务和结果。

**Architecture:** CF Worker 同时 serve 静态前端资源和代理 `/api/v1/*` 请求到 `https://async.xinbao-ai.com`。Worker 使用 CF KV 缓存已完成任务详情（succeeded/failed），通过 HMAC-SHA256 ownerHash 校验防止跨用户读取。前端使用 React + DaisyUI + Motion One 构建双栏 Dashboard。

**Tech Stack:** Vite, React 18, Tailwind CSS, DaisyUI, Motion One, Cloudflare Workers, Cloudflare KV, TypeScript

**设计文档:** `docs/plans/2026-04-12-task-dashboard-worker-design.md`

---

## 文件结构总览

```
cloudflare/task-dashboard/
├── wrangler.toml                  # CF Worker 配置 (KV binding, vars)
├── package.json                   # 依赖管理 + scripts
├── tsconfig.json                  # TypeScript 配置
├── tsconfig.worker.json           # Worker TypeScript 配置
├── vite.config.ts                 # Vite 构建配置
├── tailwind.config.js             # Tailwind + DaisyUI 配置
├── postcss.config.js              # PostCSS 配置
├── .gitignore                     # dist/, node_modules/
├── worker/
│   ├── index.ts                   # Worker 入口: 路由分发
│   ├── api-proxy.ts               # API 代理逻辑 + KV 缓存
│   ├── static.ts                  # 静态资源服务
│   └── owner-hash.ts              # HMAC-SHA256 ownerHash 派生
├── src/
│   ├── main.tsx                   # React 入口
│   ├── App.tsx                    # 根组件 (认证路由 + 主题)
│   ├── index.css                  # Tailwind 入口 + 自定义动画
│   ├── api/
│   │   └── client.ts              # fetch 封装 (auth header + 错误处理)
│   ├── hooks/
│   │   ├── useAuth.ts             # apiKey localStorage 管理
│   │   ├── useTasks.ts            # 任务列表获取
│   │   ├── useTaskDetail.ts       # 单任务详情获取
│   │   └── useTheme.ts            # 暗色/亮色切换
│   ├── components/
│   │   ├── LoginPage.tsx          # 登录页
│   │   ├── DashboardPage.tsx      # 主界面双栏布局
│   │   ├── TopBar.tsx             # 顶栏 (主题切换 + 登出)
│   │   ├── TaskList.tsx           # 左侧任务列表
│   │   ├── TaskCard.tsx           # 单个任务卡片
│   │   ├── StatusBadge.tsx        # 状态徽标
│   │   ├── TaskDetail.tsx         # 右侧详情面板
│   │   ├── ImagePreview.tsx       # 图片预览 + 灯箱
│   │   ├── MetadataPanel.tsx      # usage_metadata 面板
│   │   ├── ErrorPanel.tsx         # 错误信息面板
│   │   └── EmptyState.tsx         # 空状态提示 (列表/详情通用)
│   └── utils/
│       ├── time.ts                # Unix timestamp → 可读时间
│       └── status.ts              # 状态 → DaisyUI 类名/文案映射
└── dist/                          # 构建产物 (gitignore)
```

---

### Task 1: 项目脚手架与构建配置

**Files:**
- Create: `cloudflare/task-dashboard/package.json`
- Create: `cloudflare/task-dashboard/tsconfig.json`
- Create: `cloudflare/task-dashboard/tsconfig.worker.json`
- Create: `cloudflare/task-dashboard/vite.config.ts`
- Create: `cloudflare/task-dashboard/tailwind.config.js`
- Create: `cloudflare/task-dashboard/postcss.config.js`
- Create: `cloudflare/task-dashboard/wrangler.toml`
- Create: `cloudflare/task-dashboard/.gitignore`
- Create: `cloudflare/task-dashboard/src/main.tsx`
- Create: `cloudflare/task-dashboard/src/index.css`
- Create: `cloudflare/task-dashboard/index.html`

- [ ] **Step 1: 创建项目目录**

```bash
mkdir -p cloudflare/task-dashboard
cd cloudflare/task-dashboard
```

- [ ] **Step 2: 创建 package.json**

```json
{
  "name": "banana-task-dashboard",
  "private": true,
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite build && wrangler dev",
    "deploy": "vite build && wrangler deploy"
  },
  "dependencies": {
    "motion": "^11.18.0",
    "react": "^18.3.1",
    "react-dom": "^18.3.1"
  },
  "devDependencies": {
    "@cloudflare/workers-types": "^4.20241230.0",
    "@types/react": "^18.3.18",
    "@types/react-dom": "^18.3.5",
    "@vitejs/plugin-react": "^4.3.4",
    "autoprefixer": "^10.4.20",
    "daisyui": "^4.12.23",
    "postcss": "^8.4.49",
    "tailwindcss": "^3.4.17",
    "typescript": "^5.7.3",
    "vite": "^6.0.7",
    "wrangler": "^3.99.0"
  }
}
```

- [ ] **Step 3: 创建 tsconfig.json**

```json
{
  "compilerOptions": {
    "target": "ES2020",
    "useDefineForClassFields": true,
    "lib": ["ES2020", "DOM", "DOM.Iterable"],
    "module": "ESNext",
    "skipLibCheck": true,
    "moduleResolution": "bundler",
    "allowImportingTsExtensions": true,
    "isolatedModules": true,
    "moduleDetection": "force",
    "noEmit": true,
    "jsx": "react-jsx",
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "forceConsistentCasingInFileNames": true
  },
  "include": ["src"]
}
```

- [ ] **Step 4: 创建 tsconfig.worker.json**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022"],
    "types": ["@cloudflare/workers-types"],
    "strict": true,
    "skipLibCheck": true,
    "noEmit": true,
    "isolatedModules": true,
    "forceConsistentCasingInFileNames": true
  },
  "include": ["worker"]
}
```

- [ ] **Step 5: 创建 vite.config.ts**

```typescript
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: "dist",
    emptyDirOnBuild: true,
  },
  server: {
    proxy: {
      "/api": {
        target: "https://async.xinbao-ai.com",
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/api/, ""),
      },
    },
  },
});
```

- [ ] **Step 6: 创建 tailwind.config.js**

```javascript
/** @type {import('tailwindcss').Config} */
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  plugins: [require("daisyui")],
  daisyui: {
    themes: ["light", "dark"],
  },
};
```

- [ ] **Step 7: 创建 postcss.config.js**

```javascript
export default {
  plugins: {
    tailwindcss: {},
    autoprefixer: {},
  },
};
```

- [ ] **Step 8: 创建 wrangler.toml**

```toml
name = "task-dashboard"
main = "worker/index.ts"
compatibility_date = "2026-04-01"
account_id = "b90e43483db7d824b74157de8bf5c718"

[site]
bucket = "./dist"

[vars]
BACKEND_URL = "https://async.xinbao-ai.com"

# 部署前需要运行:
# wrangler kv namespace create TASK_CACHE
# 然后将返回的 id 填到下方
# wrangler secret put OWNER_HASH_SECRET
[[kv_namespaces]]
binding = "TASK_CACHE"
id = "placeholder-run-wrangler-kv-namespace-create"
```

- [ ] **Step 9: 创建 .gitignore**

```
node_modules/
dist/
.wrangler/
```

- [ ] **Step 10: 创建 index.html**

```html
<!DOCTYPE html>
<html lang="zh-CN" data-theme="dark">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1.0" />
    <title>Task Dashboard</title>
  </head>
  <body>
    <div id="root"></div>
    <script type="module" src="/src/main.tsx"></script>
  </body>
</html>
```

- [ ] **Step 11: 创建 src/index.css**

```css
@tailwind base;
@tailwind components;
@tailwind utilities;

/* Theme transition */
*,
*::before,
*::after {
  transition: background-color 0.3s ease, color 0.3s ease,
    border-color 0.3s ease;
}

/* Task card stagger entrance */
@keyframes card-enter {
  from {
    opacity: 0;
    transform: translateY(12px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.animate-card-enter {
  animation: card-enter 0.3s ease-out both;
}

/* Running status pulse */
@keyframes status-pulse {
  0%,
  100% {
    opacity: 1;
  }
  50% {
    opacity: 0.5;
  }
}

.animate-status-pulse {
  animation: status-pulse 2s ease-in-out infinite;
}

/* Refresh spin */
@keyframes spin {
  from {
    transform: rotate(0deg);
  }
  to {
    transform: rotate(360deg);
  }
}

.animate-spin {
  animation: spin 1s linear infinite;
}

/* Image skeleton shimmer */
@keyframes shimmer {
  0% {
    background-position: -200% 0;
  }
  100% {
    background-position: 200% 0;
  }
}

.animate-shimmer {
  background: linear-gradient(90deg, transparent 25%, oklch(var(--b3)) 50%, transparent 75%);
  background-size: 200% 100%;
  animation: shimmer 1.5s infinite;
}
```

- [ ] **Step 12: 创建 src/main.tsx (占位)**

```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import "./index.css";

function App() {
  return <div className="min-h-screen bg-base-100 text-base-content p-8">
    <h1 className="text-2xl font-bold">Task Dashboard</h1>
    <p className="mt-2 text-base-content/60">脚手架运行成功</p>
  </div>;
}

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
```

- [ ] **Step 13: 安装依赖并验证构建**

```bash
cd cloudflare/task-dashboard
npm install
npm run build
```

Expected: `dist/` 目录下生成 `index.html`、`assets/index-*.js`、`assets/index-*.css`

- [ ] **Step 14: 验证 Vite dev server 能启动**

```bash
npm run dev
# 在浏览器打开 http://localhost:5173 确认 "Task Dashboard" 页面渲染
# Ctrl+C 停止
```

- [ ] **Step 15: Commit**

```bash
git add cloudflare/task-dashboard/
git commit -m "feat(task-dashboard): scaffold project with vite, react, tailwind, daisyui"
```

---

### Task 2: Worker 入口 — 静态资源服务 + API 代理

**Files:**
- Create: `cloudflare/task-dashboard/worker/index.ts`
- Create: `cloudflare/task-dashboard/worker/static.ts`
- Create: `cloudflare/task-dashboard/worker/api-proxy.ts`

- [ ] **Step 1: 创建 worker/static.ts**

```typescript
import { getAssetFromKV } from "@cloudflare/kv-asset-handler";
// @ts-expect-error — __STATIC_CONTENT_MANIFEST is injected by wrangler at build time
import manifestJSON from "__STATIC_CONTENT_MANIFEST";

const assetManifest = JSON.parse(manifestJSON);

export interface StaticEnv {
  __STATIC_CONTENT: KVNamespace;
}

export async function serveStaticAsset(
  request: Request,
  env: StaticEnv,
  ctx: ExecutionContext
): Promise<Response> {
  const url = new URL(request.url);

  try {
    const response = await getAssetFromKV(
      { request, waitUntil: ctx.waitUntil.bind(ctx) },
      {
        ASSET_NAMESPACE: env.__STATIC_CONTENT,
        ASSET_MANIFEST: assetManifest,
      }
    );

    // Assets with hash in filename get long cache
    if (url.pathname.startsWith("/assets/")) {
      const headers = new Headers(response.headers);
      headers.set("Cache-Control", "public, max-age=31536000, immutable");
      return new Response(response.body, { ...response, headers });
    }

    // index.html: no cache
    const headers = new Headers(response.headers);
    headers.set("Cache-Control", "no-cache");
    return new Response(response.body, { ...response, headers });
  } catch {
    // SPA fallback: serve index.html for unmatched routes
    const fallbackRequest = new Request(
      new URL("/index.html", request.url).toString(),
      request
    );
    try {
      const response = await getAssetFromKV(
        { request: fallbackRequest, waitUntil: ctx.waitUntil.bind(ctx) },
        {
          ASSET_NAMESPACE: env.__STATIC_CONTENT,
          ASSET_MANIFEST: assetManifest,
        }
      );
      const headers = new Headers(response.headers);
      headers.set("Cache-Control", "no-cache");
      return new Response(response.body, { ...response, headers });
    } catch {
      return new Response("Not Found", { status: 404 });
    }
  }
}
```

- [ ] **Step 2: 创建 worker/api-proxy.ts (无 KV 缓存版本, Task 3 再加)**

```typescript
export interface ProxyEnv {
  BACKEND_URL: string;
}

export async function proxyApiRequest(
  request: Request,
  env: ProxyEnv
): Promise<Response> {
  const url = new URL(request.url);
  const backendURL = (env.BACKEND_URL || "https://async.xinbao-ai.com").replace(
    /\/$/,
    ""
  );

  // /api/v1/tasks → /v1/tasks
  const backendPath = url.pathname.replace(/^\/api/, "");
  const targetURL = backendURL + backendPath + url.search;

  const headers = new Headers();
  const authorization = request.headers.get("Authorization");
  if (authorization) {
    headers.set("Authorization", authorization);
  }
  headers.set("Content-Type", "application/json");
  headers.set("Accept", "application/json");

  const backendResponse = await fetch(targetURL, {
    method: request.method,
    headers,
    body:
      request.method !== "GET" && request.method !== "HEAD"
        ? request.body
        : undefined,
  });

  // Pass through the response, stripping hop-by-hop headers
  const responseHeaders = new Headers(backendResponse.headers);
  responseHeaders.delete("transfer-encoding");

  return new Response(backendResponse.body, {
    status: backendResponse.status,
    statusText: backendResponse.statusText,
    headers: responseHeaders,
  });
}
```

- [ ] **Step 3: 创建 worker/index.ts**

```typescript
import { serveStaticAsset, type StaticEnv } from "./static";
import { proxyApiRequest, type ProxyEnv } from "./api-proxy";

export interface Env extends StaticEnv, ProxyEnv {
  TASK_CACHE: KVNamespace;
  OWNER_HASH_SECRET: string;
}

export default {
  async fetch(
    request: Request,
    env: Env,
    ctx: ExecutionContext
  ): Promise<Response> {
    const url = new URL(request.url);

    // API proxy: /api/v1/*
    if (url.pathname.startsWith("/api/v1/")) {
      return proxyApiRequest(request, env);
    }

    // Static assets / SPA fallback
    return serveStaticAsset(request, env, ctx);
  },
};
```

- [ ] **Step 4: 添加 @cloudflare/kv-asset-handler 依赖**

```bash
cd cloudflare/task-dashboard
npm install @cloudflare/kv-asset-handler
```

- [ ] **Step 5: 验证构建 + wrangler dev 能启动**

```bash
npm run build
npx wrangler dev
# 在浏览器打开返回的 URL, 确认前端页面能加载
# Ctrl+C 停止
```

- [ ] **Step 6: Commit**

```bash
git add cloudflare/task-dashboard/worker/ cloudflare/task-dashboard/package.json cloudflare/task-dashboard/package-lock.json
git commit -m "feat(task-dashboard): worker entry with static serving and api proxy"
```

---

### Task 3: Worker KV 缓存层 + ownerHash 派生

**Files:**
- Create: `cloudflare/task-dashboard/worker/owner-hash.ts`
- Modify: `cloudflare/task-dashboard/worker/api-proxy.ts`

- [ ] **Step 1: 创建 worker/owner-hash.ts**

```typescript
/**
 * Derive ownerHash from API key using HMAC-SHA256.
 * Must match the Go backend's security.DeriveOwnerHash() exactly.
 *
 * Go implementation:
 *   mac := hmac.New(sha256.New, []byte(secret))
 *   mac.Write([]byte(token))
 *   return hex.EncodeToString(mac.Sum(nil))
 */
export async function deriveOwnerHash(
  secret: string,
  authorizationHeader: string
): Promise<string> {
  const token = normalizeBearerToken(authorizationHeader);

  const encoder = new TextEncoder();
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"]
  );

  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    encoder.encode(token)
  );

  return arrayBufferToHex(signature);
}

function normalizeBearerToken(header: string): string {
  const trimmed = header.trim();
  if (!trimmed) {
    throw new Error("authorization header is required");
  }

  const parts = trimmed.split(/\s+/);
  if (parts.length !== 2 || parts[0].toLowerCase() !== "bearer") {
    throw new Error("authorization header must use Bearer token");
  }
  if (!parts[1]) {
    throw new Error("authorization token is required");
  }

  return parts[1];
}

function arrayBufferToHex(buffer: ArrayBuffer): string {
  const bytes = new Uint8Array(buffer);
  let hex = "";
  for (let i = 0; i < bytes.length; i++) {
    hex += bytes[i].toString(16).padStart(2, "0");
  }
  return hex;
}
```

- [ ] **Step 2: 修改 worker/api-proxy.ts — 添加 KV 缓存逻辑**

完整替换 `worker/api-proxy.ts`:

```typescript
import { deriveOwnerHash } from "./owner-hash";

export interface ProxyEnv {
  BACKEND_URL: string;
  TASK_CACHE: KVNamespace;
  OWNER_HASH_SECRET: string;
}

const CACHE_TTL_SECONDS = 86400; // 24 hours
const TERMINAL_STATUSES = new Set(["succeeded", "failed"]);

/** Match /api/v1/tasks/{taskID} but not /api/v1/tasks or /api/v1/tasks/{id}/content */
function extractTaskID(pathname: string): string | null {
  const match = pathname.match(/^\/api\/v1\/tasks\/([^/]+)$/);
  if (!match || match[1] === "batch-get") return null;
  return match[1];
}

export async function proxyApiRequest(
  request: Request,
  env: ProxyEnv
): Promise<Response> {
  const url = new URL(request.url);
  const backendURL = (env.BACKEND_URL || "https://async.xinbao-ai.com").replace(
    /\/$/,
    ""
  );

  const authorization = request.headers.get("Authorization") || "";

  // KV cache: only for GET /api/v1/tasks/:id
  const taskID = request.method === "GET" ? extractTaskID(url.pathname) : null;
  if (taskID && env.TASK_CACHE && env.OWNER_HASH_SECRET) {
    try {
      const cached = await tryKVCache(env, taskID, authorization);
      if (cached) return cached;
    } catch {
      // Cache miss or error — fall through to origin
    }
  }

  // Forward to origin
  const backendPath = url.pathname.replace(/^\/api/, "");
  const targetURL = backendURL + backendPath + url.search;

  const headers = new Headers();
  if (authorization) {
    headers.set("Authorization", authorization);
  }
  headers.set("Content-Type", "application/json");
  headers.set("Accept", "application/json");

  const backendResponse = await fetch(targetURL, {
    method: request.method,
    headers,
    body:
      request.method !== "GET" && request.method !== "HEAD"
        ? request.body
        : undefined,
  });

  // If this is a successful task detail response, check if we should cache it
  if (
    taskID &&
    backendResponse.status === 200 &&
    env.TASK_CACHE &&
    env.OWNER_HASH_SECRET
  ) {
    const body = await backendResponse.text();
    try {
      const data = JSON.parse(body);
      if (data.status && TERMINAL_STATUSES.has(data.status)) {
        const ownerHash = await deriveOwnerHash(
          env.OWNER_HASH_SECRET,
          authorization
        );
        const cacheEntry = JSON.stringify({
          owner_hash: ownerHash,
          response_body: data,
          cached_at: Math.floor(Date.now() / 1000),
        });
        // Fire-and-forget write to KV
        env.TASK_CACHE.put(`task:${taskID}`, cacheEntry, {
          expirationTtl: CACHE_TTL_SECONDS,
        }).catch(() => {});
      }
    } catch {
      // JSON parse error — skip caching
    }

    // Return the response from the text we consumed
    const responseHeaders = new Headers(backendResponse.headers);
    responseHeaders.delete("transfer-encoding");
    return new Response(body, {
      status: backendResponse.status,
      statusText: backendResponse.statusText,
      headers: responseHeaders,
    });
  }

  const responseHeaders = new Headers(backendResponse.headers);
  responseHeaders.delete("transfer-encoding");
  return new Response(backendResponse.body, {
    status: backendResponse.status,
    statusText: backendResponse.statusText,
    headers: responseHeaders,
  });
}

async function tryKVCache(
  env: ProxyEnv,
  taskID: string,
  authorization: string
): Promise<Response | null> {
  const raw = await env.TASK_CACHE.get(`task:${taskID}`);
  if (!raw) return null;

  const entry = JSON.parse(raw) as {
    owner_hash: string;
    response_body: unknown;
    cached_at: number;
  };

  // Verify ownership
  const ownerHash = await deriveOwnerHash(env.OWNER_HASH_SECRET, authorization);
  if (entry.owner_hash !== ownerHash) return null;

  return new Response(JSON.stringify(entry.response_body), {
    status: 200,
    headers: {
      "Content-Type": "application/json",
      "X-Cache": "HIT",
    },
  });
}
```

- [ ] **Step 3: 验证构建**

```bash
cd cloudflare/task-dashboard
npm run build
npx wrangler dev
# Ctrl+C 停止
```

Expected: 无 TypeScript 编译错误, wrangler dev 能启动

- [ ] **Step 4: Commit**

```bash
git add cloudflare/task-dashboard/worker/
git commit -m "feat(task-dashboard): add kv cache layer with owner-hash verification"
```

---

### Task 4: 前端工具层 — API client, hooks, utils

**Files:**
- Create: `cloudflare/task-dashboard/src/api/client.ts`
- Create: `cloudflare/task-dashboard/src/hooks/useAuth.ts`
- Create: `cloudflare/task-dashboard/src/hooks/useTasks.ts`
- Create: `cloudflare/task-dashboard/src/hooks/useTaskDetail.ts`
- Create: `cloudflare/task-dashboard/src/hooks/useTheme.ts`
- Create: `cloudflare/task-dashboard/src/utils/time.ts`
- Create: `cloudflare/task-dashboard/src/utils/status.ts`

- [ ] **Step 1: 创建 src/utils/time.ts**

```typescript
/** Convert Unix timestamp (seconds) to localized readable string */
export function formatTime(unixSeconds: number): string {
  const date = new Date(unixSeconds * 1000);
  return date.toLocaleString("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false,
  });
}

/** Relative time from now, e.g. "3 分钟前" */
export function timeAgo(unixSeconds: number): string {
  const now = Math.floor(Date.now() / 1000);
  const diff = now - unixSeconds;

  if (diff < 60) return "刚刚";
  if (diff < 3600) return `${Math.floor(diff / 60)} 分钟前`;
  if (diff < 86400) return `${Math.floor(diff / 3600)} 小时前`;
  return `${Math.floor(diff / 86400)} 天前`;
}
```

- [ ] **Step 2: 创建 src/utils/status.ts**

```typescript
export type TaskStatus =
  | "accepted"
  | "queued"
  | "running"
  | "succeeded"
  | "failed"
  | "uncertain";

interface StatusConfig {
  label: string;
  badgeClass: string;
  animate: boolean;
}

const STATUS_MAP: Record<TaskStatus, StatusConfig> = {
  accepted: { label: "已接收", badgeClass: "badge-info", animate: false },
  queued: { label: "排队中", badgeClass: "badge-warning", animate: false },
  running: { label: "执行中", badgeClass: "badge-accent", animate: true },
  succeeded: { label: "成功", badgeClass: "badge-success", animate: false },
  failed: { label: "失败", badgeClass: "badge-error", animate: false },
  uncertain: { label: "不确定", badgeClass: "badge-ghost", animate: false },
};

export function getStatusConfig(status: string): StatusConfig {
  return (
    STATUS_MAP[status as TaskStatus] ?? {
      label: status,
      badgeClass: "badge-ghost",
      animate: false,
    }
  );
}

export function isTerminalStatus(status: string): boolean {
  return status === "succeeded" || status === "failed";
}
```

- [ ] **Step 3: 创建 src/api/client.ts**

```typescript
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public retryAfter?: number
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(
  path: string,
  apiKey: string,
  options?: RequestInit
): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      ...options?.headers,
    },
  });

  if (!response.ok) {
    const retryAfter = response.headers.get("Retry-After");
    let code = "unknown_error";
    let message = `HTTP ${response.status}`;

    try {
      const body = await response.json();
      if (body?.error) {
        code = body.error.code || code;
        message = body.error.message || message;
      }
    } catch {
      // Non-JSON error response
    }

    throw new ApiError(
      response.status,
      code,
      message,
      retryAfter ? parseInt(retryAfter, 10) : undefined
    );
  }

  return response.json() as Promise<T>;
}

// --- Types ---

export interface TaskListItem {
  id: string;
  model: string;
  status: string;
  created_at: number;
  finished_at?: number;
  content_url?: string;
}

export interface TaskListResponse {
  object: string;
  days: number;
  items: TaskListItem[];
}

export interface TaskDetailCandidate {
  content: {
    parts: Array<{
      text?: string;
      inlineData?: { mimeType: string; data: string };
    }>;
  };
  finishReason: string;
}

export interface TaskDetailResponse {
  id: string;
  object: string;
  model: string;
  status: string;
  created_at: number;
  finished_at?: number;
  response_id?: string;
  model_version?: string;
  usage_metadata?: Record<string, unknown>;
  candidates?: TaskDetailCandidate[];
  error?: { code: string; message: string };
  transport_uncertain?: boolean;
}

// --- API functions ---

export function fetchTaskList(apiKey: string): Promise<TaskListResponse> {
  return request<TaskListResponse>("/api/v1/tasks?limit=50", apiKey);
}

export function fetchTaskDetail(
  apiKey: string,
  taskId: string
): Promise<TaskDetailResponse> {
  return request<TaskDetailResponse>(`/api/v1/tasks/${taskId}`, apiKey);
}

/** Extract image URLs from task detail response */
export function extractImageURLs(detail: TaskDetailResponse): string[] {
  if (!detail.candidates) return [];
  const urls: string[] = [];
  for (const candidate of detail.candidates) {
    for (const part of candidate.content?.parts ?? []) {
      if (part.inlineData?.data) {
        urls.push(part.inlineData.data);
      }
    }
  }
  return urls;
}

/** Extract text content from task detail response */
export function extractTextContent(detail: TaskDetailResponse): string {
  if (!detail.candidates) return "";
  const texts: string[] = [];
  for (const candidate of detail.candidates) {
    for (const part of candidate.content?.parts ?? []) {
      if (part.text) {
        texts.push(part.text);
      }
    }
  }
  return texts.join("\n");
}
```

- [ ] **Step 4: 创建 src/hooks/useAuth.ts**

```typescript
import { useState, useCallback } from "react";

const STORAGE_KEY = "task-dashboard-api-key";

export function useAuth() {
  const [apiKey, setApiKeyState] = useState<string | null>(() =>
    localStorage.getItem(STORAGE_KEY)
  );

  const login = useCallback((key: string) => {
    localStorage.setItem(STORAGE_KEY, key);
    setApiKeyState(key);
  }, []);

  const logout = useCallback(() => {
    localStorage.removeItem(STORAGE_KEY);
    setApiKeyState(null);
  }, []);

  return {
    apiKey,
    isAuthenticated: apiKey !== null && apiKey !== "",
    login,
    logout,
  };
}
```

- [ ] **Step 5: 创建 src/hooks/useTheme.ts**

```typescript
import { useState, useCallback, useEffect } from "react";

type Theme = "light" | "dark";
const STORAGE_KEY = "task-dashboard-theme";

export function useTheme() {
  const [theme, setThemeState] = useState<Theme>(() => {
    const stored = localStorage.getItem(STORAGE_KEY);
    return stored === "light" ? "light" : "dark";
  });

  useEffect(() => {
    document.documentElement.setAttribute("data-theme", theme);
  }, [theme]);

  const toggleTheme = useCallback(() => {
    setThemeState((prev) => {
      const next = prev === "dark" ? "light" : "dark";
      localStorage.setItem(STORAGE_KEY, next);
      return next;
    });
  }, []);

  return { theme, toggleTheme };
}
```

- [ ] **Step 6: 创建 src/hooks/useTasks.ts**

```typescript
import { useState, useCallback } from "react";
import {
  fetchTaskList,
  ApiError,
  type TaskListItem,
} from "../api/client";

interface UseTasksState {
  items: TaskListItem[];
  loading: boolean;
  error: string | null;
}

export function useTasks(apiKey: string | null) {
  const [state, setState] = useState<UseTasksState>({
    items: [],
    loading: false,
    error: null,
  });

  const load = useCallback(async () => {
    if (!apiKey) return;

    setState((prev) => ({ ...prev, loading: true, error: null }));

    try {
      const data = await fetchTaskList(apiKey);
      setState({ items: data.items, loading: false, error: null });
    } catch (err) {
      const message =
        err instanceof ApiError
          ? err.message
          : err instanceof TypeError
            ? "网络连接失败，请检查网络"
            : "加载失败";
      setState((prev) => ({ ...prev, loading: false, error: message }));

      // Re-throw 401 so caller can handle logout
      if (err instanceof ApiError && err.status === 401) {
        throw err;
      }
    }
  }, [apiKey]);

  return { ...state, load };
}
```

- [ ] **Step 7: 创建 src/hooks/useTaskDetail.ts**

```typescript
import { useState, useCallback } from "react";
import {
  fetchTaskDetail,
  ApiError,
  type TaskDetailResponse,
} from "../api/client";

interface UseTaskDetailState {
  detail: TaskDetailResponse | null;
  loading: boolean;
  error: string | null;
}

export function useTaskDetail(apiKey: string | null) {
  const [state, setState] = useState<UseTaskDetailState>({
    detail: null,
    loading: false,
    error: null,
  });

  const load = useCallback(
    async (taskId: string) => {
      if (!apiKey) return;

      setState({ detail: null, loading: true, error: null });

      try {
        const data = await fetchTaskDetail(apiKey, taskId);
        setState({ detail: data, loading: false, error: null });
      } catch (err) {
        const message =
          err instanceof ApiError
            ? err.message
            : err instanceof TypeError
              ? "网络连接失败"
              : "加载详情失败";
        setState({ detail: null, loading: false, error: message });
      }
    },
    [apiKey]
  );

  const clear = useCallback(() => {
    setState({ detail: null, loading: false, error: null });
  }, []);

  return { ...state, load, clear };
}
```

- [ ] **Step 8: 验证 TypeScript 编译**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
```

Expected: 无编译错误

- [ ] **Step 9: Commit**

```bash
git add cloudflare/task-dashboard/src/
git commit -m "feat(task-dashboard): add api client, hooks, and utils"
```

---

### Task 5: LoginPage 组件

**Files:**
- Create: `cloudflare/task-dashboard/src/components/LoginPage.tsx`

- [ ] **Step 1: 创建 src/components/LoginPage.tsx**

```tsx
import { useState, type FormEvent } from "react";
import { fetchTaskList, ApiError } from "../api/client";

interface LoginPageProps {
  onLogin: (apiKey: string) => void;
}

export function LoginPage({ onLogin }: LoginPageProps) {
  const [key, setKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = key.trim();
    if (!trimmed) return;

    setLoading(true);
    setError(null);

    try {
      // Validate key by fetching 1 task
      await fetchTaskList(trimmed);
      onLogin(trimmed);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setError("API Key 无效，请检查后重试");
        } else {
          setError(err.message);
        }
      } else if (err instanceof TypeError) {
        setError("网络连接失败，请检查网络");
      } else {
        setError("验证失败，请重试");
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-base-200 px-4">
      <div className="card w-full max-w-md bg-base-100 shadow-xl">
        <div className="card-body">
          <h2 className="card-title text-2xl font-bold justify-center mb-2">
            Task Dashboard
          </h2>
          <p className="text-base-content/60 text-center text-sm mb-6">
            输入 API Key 查看最近 3 天的生图任务
          </p>

          <form onSubmit={handleSubmit}>
            <div className="form-control">
              <input
                type="password"
                placeholder="请输入 API Key"
                className="input input-bordered w-full"
                value={key}
                onChange={(e) => setKey(e.target.value)}
                disabled={loading}
                autoFocus
              />
            </div>

            {error && (
              <div className="alert alert-error mt-4 py-2 text-sm">
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  className="stroke-current shrink-0 h-5 w-5"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth="2"
                    d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <span>{error}</span>
              </div>
            )}

            <div className="form-control mt-6">
              <button
                type="submit"
                className={`btn btn-primary w-full ${loading ? "loading" : ""}`}
                disabled={loading || !key.trim()}
              >
                {loading ? "验证中..." : "查询任务"}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: 验证编译**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
```

- [ ] **Step 3: Commit**

```bash
git add cloudflare/task-dashboard/src/components/LoginPage.tsx
git commit -m "feat(task-dashboard): add login page component"
```

---

### Task 6: TopBar + StatusBadge + EmptyState 组件

**Files:**
- Create: `cloudflare/task-dashboard/src/components/TopBar.tsx`
- Create: `cloudflare/task-dashboard/src/components/StatusBadge.tsx`
- Create: `cloudflare/task-dashboard/src/components/EmptyState.tsx`

- [ ] **Step 1: 创建 src/components/StatusBadge.tsx**

```tsx
import { getStatusConfig } from "../utils/status";

interface StatusBadgeProps {
  status: string;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const config = getStatusConfig(status);

  return (
    <span
      className={`badge badge-sm ${config.badgeClass} ${
        config.animate ? "animate-status-pulse" : ""
      }`}
    >
      {config.label}
    </span>
  );
}
```

- [ ] **Step 2: 创建 src/components/TopBar.tsx**

```tsx
interface TopBarProps {
  theme: "light" | "dark";
  onToggleTheme: () => void;
  onLogout: () => void;
}

export function TopBar({ theme, onToggleTheme, onLogout }: TopBarProps) {
  return (
    <div className="navbar bg-base-100 border-b border-base-300 px-4">
      <div className="flex-1">
        <span className="text-lg font-bold">Task Dashboard</span>
      </div>
      <div className="flex-none gap-2">
        {/* Theme toggle */}
        <label className="swap swap-rotate btn btn-ghost btn-circle btn-sm">
          <input
            type="checkbox"
            checked={theme === "light"}
            onChange={onToggleTheme}
          />
          {/* Sun icon */}
          <svg
            className="swap-on fill-current w-5 h-5"
            xmlns="http://www.w3.org/2000/svg"
            viewBox="0 0 24 24"
          >
            <path d="M5.64,17l-.71.71a1,1,0,0,0,0,1.41,1,1,0,0,0,1.41,0l.71-.71A1,1,0,0,0,5.64,17ZM5,12a1,1,0,0,0-1-1H3a1,1,0,0,0,0,2H4A1,1,0,0,0,5,12Zm7-7a1,1,0,0,0,1-1V3a1,1,0,0,0-2,0V4A1,1,0,0,0,12,5ZM5.64,7.05a1,1,0,0,0,.7.29,1,1,0,0,0,.71-.29,1,1,0,0,0,0-1.41l-.71-.71A1,1,0,0,0,4.93,6.34Zm12,.29a1,1,0,0,0,.7-.29l.71-.71a1,1,0,1,0-1.41-1.41L17,5.64a1,1,0,0,0,0,1.41A1,1,0,0,0,17.66,7.34ZM21,11H20a1,1,0,0,0,0,2h1a1,1,0,0,0,0-2Zm-9,8a1,1,0,0,0-1,1v1a1,1,0,0,0,2,0V20A1,1,0,0,0,12,19ZM18.36,17A1,1,0,0,0,17,18.36l.71.71a1,1,0,0,0,1.41,0,1,1,0,0,0,0-1.41ZM12,6.5A5.5,5.5,0,1,0,17.5,12,5.51,5.51,0,0,0,12,6.5Zm0,9A3.5,3.5,0,1,1,15.5,12,3.5,3.5,0,0,1,12,15.5Z" />
          </svg>
          {/* Moon icon */}
          <svg
            className="swap-off fill-current w-5 h-5"
            xmlns="http://www.w3.org/2000/svg"
            viewBox="0 0 24 24"
          >
            <path d="M21.64,13a1,1,0,0,0-1.05-.14,8.05,8.05,0,0,1-3.37.73A8.15,8.15,0,0,1,9.08,5.49a8.59,8.59,0,0,1,.25-2A1,1,0,0,0,8,2.36,10.14,10.14,0,1,0,22,14.05,1,1,0,0,0,21.64,13Zm-9.5,6.69A8.14,8.14,0,0,1,7.08,5.22v.27A10.15,10.15,0,0,0,17.22,15.63a9.79,9.79,0,0,0,2.1-.22A8.11,8.11,0,0,1,12.14,19.73Z" />
          </svg>
        </label>

        {/* Logout */}
        <button
          className="btn btn-ghost btn-sm text-error"
          onClick={onLogout}
        >
          退出
        </button>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: 创建 src/components/EmptyState.tsx**

```tsx
interface EmptyStateProps {
  icon?: "list" | "detail";
  title: string;
  description?: string;
}

export function EmptyState({
  icon = "list",
  title,
  description,
}: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center justify-center h-full py-16 text-base-content/40">
      {icon === "list" ? (
        <svg
          xmlns="http://www.w3.org/2000/svg"
          className="h-16 w-16 mb-4"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1}
            d="M9 5H7a2 2 0 00-2 2v10a2 2 0 002 2h8a2 2 0 002-2V7a2 2 0 00-2-2h-2M9 5a2 2 0 002 2h2a2 2 0 002-2M9 5a2 2 0 012-2h2a2 2 0 012 2"
          />
        </svg>
      ) : (
        <svg
          xmlns="http://www.w3.org/2000/svg"
          className="h-16 w-16 mb-4"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
        >
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1}
            d="M15 12a3 3 0 11-6 0 3 3 0 016 0z"
          />
          <path
            strokeLinecap="round"
            strokeLinejoin="round"
            strokeWidth={1}
            d="M2.458 12C3.732 7.943 7.523 5 12 5c4.478 0 8.268 2.943 9.542 7-1.274 4.057-5.064 7-9.542 7-4.477 0-8.268-2.943-9.542-7z"
          />
        </svg>
      )}
      <h3 className="text-lg font-medium mb-1">{title}</h3>
      {description && <p className="text-sm">{description}</p>}
    </div>
  );
}
```

- [ ] **Step 4: 验证编译**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
```

- [ ] **Step 5: Commit**

```bash
git add cloudflare/task-dashboard/src/components/TopBar.tsx cloudflare/task-dashboard/src/components/StatusBadge.tsx cloudflare/task-dashboard/src/components/EmptyState.tsx
git commit -m "feat(task-dashboard): add topbar, status badge, and empty state components"
```

---

### Task 7: TaskCard + TaskList 组件

**Files:**
- Create: `cloudflare/task-dashboard/src/components/TaskCard.tsx`
- Create: `cloudflare/task-dashboard/src/components/TaskList.tsx`

- [ ] **Step 1: 创建 src/components/TaskCard.tsx**

```tsx
import { StatusBadge } from "./StatusBadge";
import { formatTime, timeAgo } from "../utils/time";
import type { TaskListItem } from "../api/client";

interface TaskCardProps {
  task: TaskListItem;
  isSelected: boolean;
  index: number;
  onClick: () => void;
}

export function TaskCard({ task, isSelected, index, onClick }: TaskCardProps) {
  return (
    <div
      className={`card card-compact cursor-pointer border transition-all duration-200 hover:border-primary/50 animate-card-enter ${
        isSelected
          ? "border-primary bg-primary/10 shadow-md"
          : "border-base-300 bg-base-100"
      }`}
      style={{ animationDelay: `${index * 50}ms` }}
      onClick={onClick}
    >
      <div className="card-body gap-1">
        <div className="flex items-center justify-between">
          <span className="font-mono text-xs text-base-content/50 truncate max-w-[140px]">
            {task.id}
          </span>
          <StatusBadge status={task.status} />
        </div>
        <div className="flex items-center justify-between mt-1">
          <span className="text-sm font-medium truncate">{task.model}</span>
          <span
            className="text-xs text-base-content/40"
            title={formatTime(task.created_at)}
          >
            {timeAgo(task.created_at)}
          </span>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: 创建 src/components/TaskList.tsx**

```tsx
import { TaskCard } from "./TaskCard";
import { EmptyState } from "./EmptyState";
import type { TaskListItem } from "../api/client";

interface TaskListProps {
  items: TaskListItem[];
  selectedId: string | null;
  loading: boolean;
  error: string | null;
  onSelect: (taskId: string) => void;
  onRefresh: () => void;
}

export function TaskList({
  items,
  selectedId,
  loading,
  error,
  onSelect,
  onRefresh,
}: TaskListProps) {
  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="flex items-center justify-between p-4 border-b border-base-300">
        <h2 className="text-sm font-semibold text-base-content/70">
          最近 3 天 · {items.length} 个任务
        </h2>
        <button
          className="btn btn-ghost btn-circle btn-sm"
          onClick={onRefresh}
          disabled={loading}
          title="刷新"
        >
          <svg
            xmlns="http://www.w3.org/2000/svg"
            className={`h-4 w-4 ${loading ? "animate-spin" : ""}`}
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path
              strokeLinecap="round"
              strokeLinejoin="round"
              strokeWidth={2}
              d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15"
            />
          </svg>
        </button>
      </div>

      {/* Error */}
      {error && (
        <div className="alert alert-error m-4 py-2 text-sm">
          <span>{error}</span>
          <button className="btn btn-ghost btn-xs" onClick={onRefresh}>
            重试
          </button>
        </div>
      )}

      {/* List */}
      <div className="flex-1 overflow-y-auto p-3 space-y-2">
        {!loading && items.length === 0 && !error && (
          <EmptyState icon="list" title="暂无任务" description="最近 3 天没有生图任务" />
        )}

        {loading && items.length === 0 && (
          <div className="flex items-center justify-center py-16">
            <span className="loading loading-spinner loading-md" />
          </div>
        )}

        {items.map((task, index) => (
          <TaskCard
            key={task.id}
            task={task}
            isSelected={task.id === selectedId}
            index={index}
            onClick={() => onSelect(task.id)}
          />
        ))}
      </div>
    </div>
  );
}
```

- [ ] **Step 3: 验证编译**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```bash
git add cloudflare/task-dashboard/src/components/TaskCard.tsx cloudflare/task-dashboard/src/components/TaskList.tsx
git commit -m "feat(task-dashboard): add task card and task list components"
```

---

### Task 8: TaskDetail + ImagePreview + MetadataPanel + ErrorPanel 组件

**Files:**
- Create: `cloudflare/task-dashboard/src/components/ImagePreview.tsx`
- Create: `cloudflare/task-dashboard/src/components/MetadataPanel.tsx`
- Create: `cloudflare/task-dashboard/src/components/ErrorPanel.tsx`
- Create: `cloudflare/task-dashboard/src/components/TaskDetail.tsx`

- [ ] **Step 1: 创建 src/components/ImagePreview.tsx**

```tsx
import { useState, useRef } from "react";

interface ImagePreviewProps {
  urls: string[];
}

export function ImagePreview({ urls }: ImagePreviewProps) {
  const [lightboxUrl, setLightboxUrl] = useState<string | null>(null);
  const [loadedMap, setLoadedMap] = useState<Record<number, boolean>>({});
  const [errorMap, setErrorMap] = useState<Record<number, boolean>>({});
  const dialogRef = useRef<HTMLDialogElement>(null);

  function openLightbox(url: string) {
    setLightboxUrl(url);
    dialogRef.current?.showModal();
  }

  function closeLightbox() {
    dialogRef.current?.close();
    setLightboxUrl(null);
  }

  if (urls.length === 0) return null;

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        {urls.map((url, i) => (
          <div key={i} className="relative">
            {/* Skeleton */}
            {!loadedMap[i] && !errorMap[i] && (
              <div className="aspect-square rounded-lg animate-shimmer bg-base-300" />
            )}

            {/* Error state */}
            {errorMap[i] && (
              <div className="aspect-square rounded-lg bg-base-300 flex flex-col items-center justify-center gap-2">
                <span className="text-base-content/40 text-sm">加载失败</span>
                <button
                  className="btn btn-ghost btn-xs"
                  onClick={() => {
                    setErrorMap((prev) => ({ ...prev, [i]: false }));
                    setLoadedMap((prev) => ({ ...prev, [i]: false }));
                  }}
                >
                  重试
                </button>
              </div>
            )}

            {/* Image */}
            {!errorMap[i] && (
              <img
                src={url}
                alt={`Generated image ${i + 1}`}
                className={`w-full rounded-lg cursor-pointer hover:ring-2 hover:ring-primary transition-all duration-400 ${
                  loadedMap[i] ? "opacity-100" : "opacity-0 absolute inset-0"
                }`}
                onLoad={() =>
                  setLoadedMap((prev) => ({ ...prev, [i]: true }))
                }
                onError={() =>
                  setErrorMap((prev) => ({ ...prev, [i]: true }))
                }
                onClick={() => openLightbox(url)}
              />
            )}
          </div>
        ))}
      </div>

      {/* Lightbox dialog */}
      <dialog ref={dialogRef} className="modal" onClick={closeLightbox}>
        <div className="modal-box max-w-5xl p-2 bg-base-300">
          {lightboxUrl && (
            <img
              src={lightboxUrl}
              alt="Full size preview"
              className="w-full rounded"
            />
          )}
        </div>
        <form method="dialog" className="modal-backdrop">
          <button>close</button>
        </form>
      </dialog>
    </>
  );
}
```

- [ ] **Step 2: 创建 src/components/MetadataPanel.tsx**

```tsx
interface MetadataPanelProps {
  metadata: Record<string, unknown>;
}

export function MetadataPanel({ metadata }: MetadataPanelProps) {
  const entries = Object.entries(metadata);
  if (entries.length === 0) return null;

  return (
    <div className="collapse collapse-arrow bg-base-200 rounded-lg">
      <input type="checkbox" />
      <div className="collapse-title text-sm font-medium">
        Usage Metadata
      </div>
      <div className="collapse-content">
        <div className="overflow-x-auto">
          <table className="table table-xs">
            <tbody>
              {entries.map(([key, value]) => (
                <tr key={key}>
                  <td className="font-mono text-xs text-base-content/60 whitespace-nowrap">
                    {key}
                  </td>
                  <td className="text-xs">
                    {typeof value === "object"
                      ? JSON.stringify(value)
                      : String(value)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: 创建 src/components/ErrorPanel.tsx**

```tsx
interface ErrorPanelProps {
  code: string;
  message: string;
  uncertain?: boolean;
}

export function ErrorPanel({ code, message, uncertain }: ErrorPanelProps) {
  return (
    <div className="alert alert-error">
      <div className="flex flex-col gap-1">
        <div className="flex items-center gap-2">
          <span className="font-mono text-xs badge badge-outline">{code}</span>
          {uncertain && (
            <span className="badge badge-warning badge-xs">transport uncertain</span>
          )}
        </div>
        <p className="text-sm">{message}</p>
      </div>
    </div>
  );
}
```

- [ ] **Step 4: 创建 src/components/TaskDetail.tsx**

```tsx
import { useEffect, useRef } from "react";
import { animate } from "motion";
import { StatusBadge } from "./StatusBadge";
import { ImagePreview } from "./ImagePreview";
import { MetadataPanel } from "./MetadataPanel";
import { ErrorPanel } from "./ErrorPanel";
import { EmptyState } from "./EmptyState";
import { formatTime } from "../utils/time";
import {
  extractImageURLs,
  extractTextContent,
  type TaskDetailResponse,
} from "../api/client";

interface TaskDetailProps {
  detail: TaskDetailResponse | null;
  loading: boolean;
  error: string | null;
  onRetry?: () => void;
  /** Mobile: show back button */
  onBack?: () => void;
}

export function TaskDetail({
  detail,
  loading,
  error,
  onRetry,
  onBack,
}: TaskDetailProps) {
  const containerRef = useRef<HTMLDivElement>(null);

  // Animate panel content on detail change
  useEffect(() => {
    if (containerRef.current && detail) {
      animate(
        containerRef.current,
        { opacity: [0, 1], x: [20, 0] },
        { duration: 0.2, easing: "ease-out" }
      );
    }
  }, [detail?.id]);

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full">
        <span className="loading loading-spinner loading-lg" />
      </div>
    );
  }

  if (error) {
    return (
      <div className="flex flex-col items-center justify-center h-full gap-3">
        <p className="text-error text-sm">{error}</p>
        {onRetry && (
          <button className="btn btn-ghost btn-sm" onClick={onRetry}>
            重试
          </button>
        )}
      </div>
    );
  }

  if (!detail) {
    return (
      <EmptyState
        icon="detail"
        title="选择一个任务"
        description="点击左侧任务卡片查看详情"
      />
    );
  }

  const imageURLs = extractImageURLs(detail);
  const textContent = extractTextContent(detail);

  return (
    <div ref={containerRef} className="h-full overflow-y-auto p-4 space-y-4">
      {/* Mobile back button */}
      {onBack && (
        <button className="btn btn-ghost btn-sm mb-2 lg:hidden" onClick={onBack}>
          <svg
            xmlns="http://www.w3.org/2000/svg"
            className="h-4 w-4"
            fill="none"
            viewBox="0 0 24 24"
            stroke="currentColor"
          >
            <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15 19l-7-7 7-7" />
          </svg>
          返回列表
        </button>
      )}

      {/* Header */}
      <div className="flex items-center justify-between flex-wrap gap-2">
        <div>
          <h3 className="font-mono text-sm text-base-content/50">{detail.id}</h3>
          <p className="text-lg font-semibold">{detail.model}</p>
        </div>
        <StatusBadge status={detail.status} />
      </div>

      {/* Time info */}
      <div className="flex gap-4 text-xs text-base-content/50">
        <span>创建: {formatTime(detail.created_at)}</span>
        {detail.finished_at && <span>完成: {formatTime(detail.finished_at)}</span>}
        {detail.model_version && <span>版本: {detail.model_version}</span>}
      </div>

      {/* Success content */}
      {detail.status === "succeeded" && (
        <>
          {textContent && (
            <div className="bg-base-200 rounded-lg p-3 text-sm whitespace-pre-wrap">
              {textContent}
            </div>
          )}
          <ImagePreview urls={imageURLs} />
        </>
      )}

      {/* Error content */}
      {(detail.status === "failed" || detail.status === "uncertain") &&
        detail.error && (
          <ErrorPanel
            code={detail.error.code}
            message={detail.error.message}
            uncertain={detail.transport_uncertain}
          />
        )}

      {/* Metadata */}
      {detail.usage_metadata && (
        <MetadataPanel metadata={detail.usage_metadata} />
      )}
    </div>
  );
}
```

- [ ] **Step 5: 验证编译**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
```

- [ ] **Step 6: Commit**

```bash
git add cloudflare/task-dashboard/src/components/ImagePreview.tsx cloudflare/task-dashboard/src/components/MetadataPanel.tsx cloudflare/task-dashboard/src/components/ErrorPanel.tsx cloudflare/task-dashboard/src/components/TaskDetail.tsx
git commit -m "feat(task-dashboard): add task detail, image preview, metadata, and error panels"
```

---

### Task 9: DashboardPage 组件 + App 组装

**Files:**
- Create: `cloudflare/task-dashboard/src/components/DashboardPage.tsx`
- Modify: `cloudflare/task-dashboard/src/App.tsx`
- Modify: `cloudflare/task-dashboard/src/main.tsx`

- [ ] **Step 1: 创建 src/components/DashboardPage.tsx**

```tsx
import { useState, useEffect, useCallback } from "react";
import { TopBar } from "./TopBar";
import { TaskList } from "./TaskList";
import { TaskDetail } from "./TaskDetail";
import { useTasks } from "../hooks/useTasks";
import { useTaskDetail } from "../hooks/useTaskDetail";
import { useTheme } from "../hooks/useTheme";
import { ApiError } from "../api/client";

interface DashboardPageProps {
  apiKey: string;
  onLogout: () => void;
}

export function DashboardPage({ apiKey, onLogout }: DashboardPageProps) {
  const { theme, toggleTheme } = useTheme();
  const tasks = useTasks(apiKey);
  const taskDetail = useTaskDetail(apiKey);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showDetail, setShowDetail] = useState(false); // mobile: toggle detail view

  // Load tasks on mount
  useEffect(() => {
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) {
        onLogout();
      }
    });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const handleRefresh = useCallback(() => {
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) {
        onLogout();
      }
    });
  }, [tasks.load, onLogout]);

  const handleSelect = useCallback(
    (taskId: string) => {
      setSelectedId(taskId);
      setShowDetail(true);
      taskDetail.load(taskId);
    },
    [taskDetail.load]
  );

  const handleBack = useCallback(() => {
    setShowDetail(false);
    taskDetail.clear();
  }, [taskDetail.clear]);

  const handleDetailRetry = useCallback(() => {
    if (selectedId) {
      taskDetail.load(selectedId);
    }
  }, [selectedId, taskDetail.load]);

  return (
    <div className="h-screen flex flex-col bg-base-200">
      <TopBar
        theme={theme}
        onToggleTheme={toggleTheme}
        onLogout={onLogout}
      />

      <div className="flex-1 flex overflow-hidden">
        {/* Left: Task list */}
        <div
          className={`w-full lg:w-2/5 xl:w-[40%] border-r border-base-300 bg-base-100 flex-shrink-0 ${
            showDetail ? "hidden lg:flex lg:flex-col" : "flex flex-col"
          }`}
        >
          <TaskList
            items={tasks.items}
            selectedId={selectedId}
            loading={tasks.loading}
            error={tasks.error}
            onSelect={handleSelect}
            onRefresh={handleRefresh}
          />
        </div>

        {/* Right: Task detail */}
        <div
          className={`flex-1 bg-base-100 ${
            showDetail ? "flex flex-col" : "hidden lg:flex lg:flex-col"
          }`}
        >
          <TaskDetail
            detail={taskDetail.detail}
            loading={taskDetail.loading}
            error={taskDetail.error}
            onRetry={handleDetailRetry}
            onBack={handleBack}
          />
        </div>
      </div>
    </div>
  );
}
```

- [ ] **Step 2: 创建 src/App.tsx**

```tsx
import { useAuth } from "./hooks/useAuth";
import { LoginPage } from "./components/LoginPage";
import { DashboardPage } from "./components/DashboardPage";

export function App() {
  const { apiKey, isAuthenticated, login, logout } = useAuth();

  if (!isAuthenticated || !apiKey) {
    return <LoginPage onLogin={login} />;
  }

  return <DashboardPage apiKey={apiKey} onLogout={logout} />;
}
```

- [ ] **Step 3: 修改 src/main.tsx**

```tsx
import React from "react";
import ReactDOM from "react-dom/client";
import { App } from "./App";
import "./index.css";

ReactDOM.createRoot(document.getElementById("root")!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>
);
```

- [ ] **Step 4: 验证完整编译和构建**

```bash
cd cloudflare/task-dashboard
npx tsc --noEmit
npm run build
```

Expected: 编译无错误, `dist/` 下有 `index.html` + `assets/` 文件

- [ ] **Step 5: Commit**

```bash
git add cloudflare/task-dashboard/src/
git commit -m "feat(task-dashboard): add dashboard page and wire up app with auth routing"
```

---

### Task 10: 集成验证 + 清理

**Files:**
- Verify: all files in `cloudflare/task-dashboard/`

- [ ] **Step 1: 完整构建验证**

```bash
cd cloudflare/task-dashboard
npm run build
```

Expected: 构建成功, `dist/` 下有 `index.html` 和 `assets/` 目录

- [ ] **Step 2: wrangler dev 本地验证**

```bash
npx wrangler dev
```

Expected: 能启动并访问前端页面。API 代理需要后端可达才能测试。

- [ ] **Step 3: 验证页面功能**

在浏览器中:
1. 打开 Worker URL → 看到登录页
2. 输入无效 API key → 显示错误提示
3. 输入有效 API key → 进入 Dashboard, 看到任务列表
4. 点击任务卡片 → 右侧显示详情
5. 成功任务 → 显示图片
6. 失败任务 → 显示错误信息
7. 点击主题切换 → 暗色/亮色切换
8. 点击退出 → 回到登录页
9. 刷新页面 → 自动登录 (localStorage)
10. 缩小浏览器窗口到手机宽度 → 单栏布局

- [ ] **Step 4: 检查产物体积**

```bash
ls -lah dist/assets/
```

Expected: JS + CSS 合计 gzip 后 < 100 KB

- [ ] **Step 5: 最终 Commit**

```bash
git add cloudflare/task-dashboard/
git commit -m "feat(task-dashboard): complete task dashboard with api proxy, kv cache, and responsive ui"
```

---

## 部署 Checklist (非自动化, 手动执行)

以下步骤需要在首次部署时手动执行:

1. **创建 KV namespace:**
   ```bash
   cd cloudflare/task-dashboard
   npx wrangler kv namespace create TASK_CACHE
   ```
   将返回的 `id` 填入 `wrangler.toml` 的 `[[kv_namespaces]]` 段

2. **配置 Secret:**
   ```bash
   npx wrangler secret put OWNER_HASH_SECRET
   # 输入与 async-gateway 相同的 OWNER_HASH_SECRET 值
   ```

3. **部署:**
   ```bash
   npm run deploy
   ```

4. **验证:**
   访问 `https://task-dashboard.<your-subdomain>.workers.dev` 确认页面可访问
