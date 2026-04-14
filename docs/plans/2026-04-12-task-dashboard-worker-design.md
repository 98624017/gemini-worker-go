# Task Dashboard Worker 设计文档

**日期**: 2026-04-12
**状态**: 待实现

---

## 一、背景与目标

为 async-gateway 的终端用户提供一个可视化 Dashboard，用户在网页中输入 API key 即可查询最近 3 天的所有生图任务及生成结果。

**核心目标：**
- 用户友好的任务查询界面（无需 curl / Postman）
- 隐藏 async-gateway 后端地址
- 利用 CF 边缘能力减少回源请求

---

## 二、技术栈

| 层级 | 技术选型 | 理由 |
|------|---------|------|
| 运行时 | Cloudflare Worker | 统一托管前端 + API 代理，同源无 CORS |
| 边缘缓存 | Cloudflare KV | 缓存已完成任务详情，减少回源 |
| 前端框架 | React 18 | 组件化管理，后续扩展方便 |
| UI 组件库 | DaisyUI | 语义化类名，30+ 内置主题，零 JS 运行时 |
| CSS 框架 | Tailwind CSS | DaisyUI 依赖，purge 后极小 |
| 动效 | Motion One (~3.5KB) | 框架无关，轻量级动画库 |
| 构建工具 | Vite | 极快构建，原生支持 Worker 产物 |
| 包管理 | npm | 与现有 r2-cleaner 一致 |

**构建产物体积预估 (gzip)：**

| 部分 | 大小 |
|------|------|
| React + ReactDOM | ~45 KB |
| DaisyUI CSS (2 主题) | ~15 KB |
| Tailwind (purged) | ~8 KB |
| Motion One | ~3.5 KB |
| 业务代码 | ~10 KB |
| **合计** | **~82 KB** |

---

## 三、整体架构

```
用户浏览器
  |
  |-- GET /                      -> Worker 返回 index.html
  |-- GET /assets/*              -> Worker 返回 JS/CSS 静态资源
  +-- GET/POST /api/v1/*         -> Worker 代理到 async-gateway
        |
        |  Worker 处理层:
        |  1. 静态资源托管 (Vite 构建产物)
        |  2. API 代理 (附加 Authorization header, 隐藏后端地址)
        |  3. KV 缓存 (已完成任务详情, TTL 24h)
        |  4. 同源部署, 无 CORS 问题
        |
        v
  async-gateway (Go 后端: https://async.xinbao-ai.com)
        |
        v
  PostgreSQL
```

### 3.1 Worker 路由表

| 路由 | 方法 | 行为 |
|------|------|------|
| `/` | GET | 返回 `index.html` |
| `/assets/*` | GET | 返回 Vite 构建的 JS/CSS (带 hash, 长缓存) |
| `/api/v1/tasks` | GET | 代理到 `BACKEND_URL/v1/tasks?limit=50` (始终回源) |
| `/api/v1/tasks/:id` | GET | 先查 KV 缓存, 未命中或未完成则回源 |
| `/api/v1/tasks/:id/content` | GET | 代理到后端, 返回 302 目标 URL 给前端 |

### 3.2 Worker 环境变量

| 变量 | 说明 | 默认值 |
|------|------|--------|
| `BACKEND_URL` | async-gateway 后端地址 | `https://async.xinbao-ai.com` |

### 3.3 Worker KV Binding

| Binding 名 | 用途 |
|------------|------|
| `TASK_CACHE` | 缓存已完成任务详情 |

---

## 四、KV 边缘缓存策略

### 4.1 缓存逻辑

```
GET /api/v1/tasks/:id 请求进入 Worker
  |
  +-- 从请求 header 提取 Authorization (Bearer token)
  |
  +-- 构建 KV key: "task:{taskID}"
  |
  +-- 查 KV
      |-- 命中 -> 解析缓存数据
      |   |-- 验证 ownerHash 匹配 (防止跨用户读取)
      |   +-- 直接返回 (不回源)
      |
      +-- 未命中 -> 回源 async-gateway
          |-- 获取响应
          |-- 如果状态为 succeeded 或 failed:
          |   +-- 写入 KV (TTL: 24h, 与后端 3 天窗口一致, 留余量)
          +-- 返回响应
```

### 4.2 缓存数据结构

```json
{
  "owner_hash": "hmac-sha256-hex",
  "response_body": { /* 原始 API 响应 JSON */ },
  "cached_at": 1712900000
}
```

### 4.3 安全约束

- KV value 中包含 `owner_hash`, Worker 验证请求者的 apiKey 派生的 hash 与缓存一致
- 防止用户 A 的缓存被用户 B 读取
- Worker 需要实现与后端相同的 `DeriveOwnerHash(secret, token)` 逻辑
- Worker 需要配置 `OWNER_HASH_SECRET` 环境变量 (与 async-gateway 一致)

### 4.4 缓存范围

| 对象 | 是否缓存 | 理由 |
|------|---------|------|
| 已完成任务详情 (succeeded/failed) | 缓存, TTL 24h | 状态不可变 |
| 进行中任务 (accepted/queued/running) | 不缓存 | 状态随时变化 |
| uncertain 任务 | 不缓存 | 状态可能后续变更 |
| 任务列表 | 不缓存 | 每次刷新期望最新数据 |
| /content 重定向 | 不缓存 | URL 可能过期 |

---

## 五、前端页面设计

### 5.1 页面状态流

```
[启动] -- localStorage 有 apiKey? --> [主界面]
  |                                       |
  |  无 apiKey                            v
  v                                  [双栏布局]
[登录页]                             |- 左侧: 任务列表
  |                                  |- 右侧: 详情面板
  |  输入 apiKey, 点击查询            +- 顶栏: 主题切换 + 登出
  |  调 /api/v1/tasks?limit=1 验证
  |
  |-- 成功 -> 存 localStorage -> 进入主界面
  +-- 失败 -> 显示错误提示, 留在登录页
```

### 5.2 组件拆分

```
App
|-- LoginPage                    # API key 输入页
|   |-- KeyInput                 # 输入框 + 提交按钮
|   +-- ErrorAlert               # 错误提示 (DaisyUI alert)
|
|-- DashboardPage                # 主界面 (双栏)
|   |-- TopBar                   # 顶栏
|   |   |-- ThemeToggle          # 暗色/亮色切换 (DaisyUI swap)
|   |   +-- LogoutButton         # 清除 apiKey, 回登录页
|   |
|   |-- TaskList                 # 左侧任务列表
|   |   |-- RefreshButton        # 手动刷新按钮
|   |   |-- TaskCard[]           # 任务卡片 (点击选中)
|   |   |   |-- StatusBadge      # 状态徽标 (颜色区分)
|   |   |   |-- ModelLabel       # 模型名称
|   |   |   +-- TimeLabel        # 创建/完成时间
|   |   +-- EmptyState           # 无任务时的空状态提示
|   |
|   +-- TaskDetail               # 右侧详情面板
|       |-- DetailHeader         # 任务 ID + 状态
|       |-- ImagePreview         # 图片预览 (点击放大)
|       |-- MetadataPanel        # usage_metadata 展示
|       |-- ErrorPanel           # 失败任务的错误信息
|       +-- EmptyDetail          # 未选中任务时的引导提示
|
+-- ImageLightbox                # 图片放大灯箱 (全局)
```

### 5.3 状态徽标颜色映射

| 状态 | DaisyUI 类 | 含义 |
|------|-----------|------|
| `accepted` | `badge-info` | 已接收 |
| `queued` | `badge-warning` | 排队中 |
| `running` | `badge-accent` + 呼吸动画 | 执行中 |
| `succeeded` | `badge-success` | 成功 |
| `failed` | `badge-error` | 失败 |
| `uncertain` | `badge-ghost` | 不确定 |

---

## 六、数据流

### 6.1 登录流程

1. 用户输入 apiKey
2. 前端调 `GET /api/v1/tasks?limit=1` 验证 key 有效性
3. 成功 (200) -> apiKey 存 localStorage -> 跳转主界面
4. 失败 (401) -> 显示 "API Key 无效"
5. 失败 (其他) -> 显示具体错误信息

### 6.2 主界面数据流

```
进入主界面
  |
  +-- 自动调 GET /api/v1/tasks?limit=50
  |   响应:
  |   {
  |     "object": "list",
  |     "days": 3,
  |     "items": [
  |       { "id": "xxx", "model": "imagen-3.0", "status": "succeeded",
  |         "created_at": 1712900000, "finished_at": 1712900060 },
  |       ...
  |     ]
  |   }
  |
  +-- 渲染任务列表 (按 created_at 倒序, 后端已排序)
  |
  +-- 用户点击某个任务卡片
      |
      +-- 调 GET /api/v1/tasks/:id (可能命中 KV 缓存)
      |   响应包含:
      |   - candidates[].content.parts[].inlineData.data -> 图片 URL
      |   - usage_metadata -> token 用量
      |   - error / error_message -> 错误信息
      |
      +-- 渲染详情面板
          |-- 成功任务: 图片 <img src="图床URL"> + usage_metadata
          +-- 失败任务: error_code + error_message
```

### 6.3 图片加载策略

- 列表视图: 不加载图片 (节省带宽)
- 详情视图: 从 task detail 响应中提取图床 URL, 直接 `<img>` 加载
- 加载中: skeleton 占位动画
- 加载成功: 渐入动画
- 加载失败: 错误占位图 + 重试按钮

### 6.4 手动刷新

- 点击刷新按钮 -> 旋转动画
- 重新调 `GET /api/v1/tasks?limit=50`
- 列表更新 (带入场动画)
- 当前选中任务仍在列表中 -> 保持选中; 否则清空详情面板

---

## 七、动效设计

| 交互场景 | 动效方案 | 实现方式 | 时长 |
|----------|---------|---------|------|
| 任务卡片入场 | 从下方渐入, 依次错开 | CSS `@keyframes` + `animation-delay` | 300ms, 每项错开 50ms |
| 点击任务卡片 | 卡片高亮 + 详情面板滑入 | DaisyUI `active` + Motion One `animate()` | 250ms |
| 详情面板切换 | 内容淡出旧 -> 淡入新 | Motion One `animate()` | 200ms |
| 图片加载完成 | skeleton -> 图片渐入 | CSS `opacity` transition | 400ms |
| 图片放大灯箱 | 从缩略图放大到全屏 | Motion One `animate()` scale + position | 300ms |
| 刷新按钮 | 图标旋转 | CSS `animation: spin` | 请求期间持续 |
| 主题切换 | 全局颜色平滑过渡 | `* { transition: background-color 0.3s, color 0.3s }` | 300ms |
| 状态徽标 running | 呼吸闪烁 | CSS `@keyframes pulse` | 2s 循环 |
| 登录成功 | 登录页淡出 -> 主界面淡入 | View Transitions API | 400ms |
| 错误提示 | 从顶部滑入, 3 秒后自动消失 | Motion One + setTimeout | 300ms 入 / 300ms 出 |

---

## 八、主题系统

### 8.1 配置

```javascript
// tailwind.config.js
module.exports = {
  plugins: [require("daisyui")],
  daisyui: {
    themes: ["light", "dark"],
  }
}
```

### 8.2 切换逻辑

1. 页面加载 -> 读 `localStorage("theme")` -> 默认 `"dark"`
2. 点击 ThemeToggle -> 切换 `<html data-theme>` -> 存 localStorage
3. DaisyUI 自动应用对应主题色
4. 全局 `transition: background-color 0.3s, color 0.3s` 平滑过渡

---

## 九、响应式布局

| 屏幕宽度 | 布局 |
|----------|------|
| >= 1024px | 双栏: 左侧列表 40% + 右侧详情 60% |
| 768px ~ 1023px | 双栏: 左侧列表 45% + 右侧详情 55% |
| < 768px | 单栏: 列表全屏, 点击任务后详情全屏覆盖, 带返回按钮 |

---

## 十、项目目录结构

```
cloudflare/task-dashboard/
|-- wrangler.toml                 # CF Worker 配置 (含 KV binding)
|-- package.json                  # 依赖管理
|-- vite.config.ts                # Vite 构建配置
|-- tailwind.config.js            # Tailwind + DaisyUI 配置
|-- postcss.config.js             # PostCSS 配置
|-- worker/
|   +-- index.ts                  # Worker 入口: 静态资源 + API 代理 + KV 缓存
|-- src/
|   |-- main.tsx                  # React 入口
|   |-- App.tsx                   # 根组件 (路由, 主题, 全局状态)
|   |-- api/
|   |   +-- client.ts             # API 封装 (fetch + auth header + 错误处理)
|   |-- hooks/
|   |   |-- useAuth.ts            # apiKey 管理 (localStorage 读写)
|   |   |-- useTasks.ts           # 任务列表数据获取
|   |   |-- useTaskDetail.ts      # 单任务详情获取
|   |   +-- useTheme.ts           # 主题切换
|   |-- components/
|   |   |-- LoginPage.tsx
|   |   |-- DashboardPage.tsx
|   |   |-- TopBar.tsx
|   |   |-- TaskList.tsx
|   |   |-- TaskCard.tsx
|   |   |-- StatusBadge.tsx
|   |   |-- TaskDetail.tsx
|   |   |-- ImagePreview.tsx
|   |   |-- ImageLightbox.tsx
|   |   |-- MetadataPanel.tsx
|   |   |-- ErrorPanel.tsx
|   |   |-- EmptyState.tsx
|   |   +-- RefreshButton.tsx
|   |-- utils/
|   |   |-- time.ts               # 时间格式化 (Unix -> 可读)
|   |   +-- status.ts             # 状态映射 (状态 -> 颜色/文案)
|   +-- index.css                 # Tailwind 入口 + 自定义动画 keyframes
+-- dist/                         # Vite 构建产物 (gitignore)
```

---

## 十一、错误处理

| 场景 | 用户看到的 | 行为 |
|------|-----------|------|
| API key 无效 (401) | "API Key 无效, 请检查后重试" | 留在登录页 |
| 登录态下收到 401 | "会话已过期, 请重新输入 API Key" | 清除 localStorage, 跳回登录页 |
| 网络错误 | "网络连接失败, 请检查网络" | 保持当前页面, 可重试 |
| 后端 500 | "服务暂时不可用, 请稍后重试" | 保持当前页面, 可重试 |
| 429 速率限制 | "请求过于频繁, {retryAfter}秒后重试" | 刷新按钮禁用倒计时 |
| 任务详情加载失败 | 详情面板显示错误提示 | 可点击重试 |
| 图片加载失败 | broken image 占位 + "加载失败" | 可点击重试 |

---

## 十二、安全考量

- **API key 不经 Worker 持久化**: Worker 仅透传 Authorization header
- **BACKEND_URL 不暴露给前端**: 前端只知道 `/api/v1/*`, Worker 负责转发
- **KV 缓存含 ownerHash 校验**: 防止跨用户读取缓存
- **静态资源缓存**: JS/CSS 文件名含 hash, `Cache-Control: max-age=31536000`; `index.html` 设 `no-cache`
- **Worker 不记录 API key**: 日志中脱敏处理
- **OWNER_HASH_SECRET 配置为 Worker Secret**: 不明文出现在 wrangler.toml

---

## 十三、部署

### 13.1 Wrangler 配置

```toml
name = "task-dashboard"
main = "worker/index.ts"
compatibility_date = "2024-12-01"

[site]
bucket = "./dist"

[vars]
BACKEND_URL = "https://async.xinbao-ai.com"

[[kv_namespaces]]
binding = "TASK_CACHE"
id = "<kv-namespace-id>"
```

### 13.2 开发工作流

```bash
# 本地开发 (前端热更新)
npm run dev

# Worker 本地模拟
npm run dev:worker

# 完整本地预览
npm run preview    # vite build + wrangler dev

# 部署
npm run deploy     # vite build && wrangler deploy
```

### 13.3 首次部署前置步骤

1. `wrangler kv namespace create TASK_CACHE` -- 创建 KV namespace
2. 将返回的 id 填入 wrangler.toml
3. `wrangler secret put OWNER_HASH_SECRET` -- 配置 secret (与 async-gateway 一致)
4. `npm run deploy`
