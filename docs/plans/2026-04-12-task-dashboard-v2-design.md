# Task Dashboard V2 设计文档

**日期**: 2026-04-12
**状态**: 待实现
**前置**: `docs/plans/2026-04-12-task-dashboard-worker-design.md` (V1 已实现)

---

## 一、背景

V1 已实现基础功能：登录、任务列表、详情面板、主题切换。基于真实数据的 E2E 测试暴露以下问题并收集了新需求：

**UI 问题：**
- 任务卡片过大，一屏仅显示 8-9 个，信息密度低
- 模型名过长占据空间

**功能缺失：**
- 无法在 Dashboard 内更换 API Key
- 任务卡片无耗时信息
- 无任务记录/图片有效期提示
- 仅加载 50 条，无分页
- 无筛选功能
- 无相册/看图模式
- 无图片下载功能

---

## 二、变更范围

| 模块 | 类型 | 说明 |
|------|------|------|
| UI 密度优化 | 改造 | 卡片压缩、间距缩减、字号调小 |
| TopBar | 改造 | 增加视图切换 + API Key 编辑 |
| 任务列表 | 改造 | 分页、筛选、耗时、提示文案 |
| 相册视图 | 新增 | 瀑布流、批量加载、下载 |
| 下载工具 | 新增 | 单张下载 + JSZip 批量打包 |

---

## 三、UI 密度优化

### 3.1 任务卡片

**现状（每卡片 ~76px）：**
```
┌─────────────────────────────────────────┐
│  img_66hqlupgrwb7af...          [成功]   │  ← 第一行：ID + 状态
│  gemini-3-pro-image-preview-svip        │  ← 第二行：模型 + 时间
│                                 1小时前  │
└─────────────────────────────────────────┘
   ↕ 8px 间距
```

**优化后（每卡片 ~34px）：**
```
[svip] img_66hqlupgrwb7af...  1小时前 [成功]    ← 单行扁平
 ↕ 2px 间距
```

### 3.2 具体改动

| 元素 | 现状 | 优化后 |
|------|------|--------|
| 卡片容器 | `card card-compact` + `card-body` | `flex items-center` 扁平行 |
| 内边距 | card-body 默认 ~12px | `px-2.5 py-1.5` |
| 卡片间距 | `space-y-2` (8px) | `space-y-0.5` (2px) |
| 状态徽标 | `badge-sm` | `badge-xs text-[10px]` |
| 字号 | `text-xs` / `text-sm` | 统一 `text-[11px]` |
| 列表头 | `p-4` | `px-3 py-2` |
| 模型名 | 完整 `gemini-3-pro-image-preview-svip` | 截取后缀 `svip` / `vip`，badge 形式 |
| 入场动画 | 每项 50ms delay，50 项 = 2.5s | 上限 15 项 x 30ms = 450ms |

**预估效果：** 768px 视口一屏可显示 ~20 个任务（现状 ~8-9）。

---

## 四、TopBar 改造

### 4.1 布局

```
┌──────────────────────────────────────────────────────────────┐
│ Task Dashboard   [任务|相册]    [sk-****Lhs ✏️]   [🌙] [退出] │
└──────────────────────────────────────────────────────────────┘
```

### 4.2 视图切换

- DaisyUI `btn-group` 两个按钮：`任务` / `相册`
- 当前视图高亮（`btn-active`）
- 点击切换 DashboardPage 的 `view` state

### 4.3 API Key 编辑

- 默认显示掩码：`sk-****{后4位}`
- 点击 ✏️ 图标展开内联输入框
- 输入新 key 后点"确认"：
  1. 调 `GET /api/v1/tasks?limit=1` 验证
  2. 成功 → 更新 localStorage + 重新加载数据
  3. 失败 → toast 提示错误，保留旧 key
- 按 Esc 或点其他区域 → 取消编辑

---

## 五、任务视图增强

### 5.1 分页

**参数变更：**
- `limit` 从 50 改为 100（后端最大值）
- 新增游标参数 `before_created_at` + `before_id`

**交互流程：**
```
初始加载 GET /api/v1/tasks?limit=100
  |
  +-- 渲染列表
  |
  +-- items.length === 100?
      |-- 是 → 显示"加载更多"按钮
      |   +-- 点击 → GET /api/v1/tasks?limit=100
      |   |     &before_created_at={lastItem.created_at}
      |   |     &before_id={lastItem.id}
      |   +-- 追加到现有列表
      |   +-- 再次判断是否还有更多
      |
      +-- 否 → 显示"已加载全部"提示
```

**useTasks hook 变更：**
- 新增 `loadMore()` 方法
- 新增 `hasMore: boolean` 状态
- `load()` 重置列表；`loadMore()` 追加

### 5.2 筛选

**筛选维度：**

| 维度 | 实现 | UI |
|------|------|-----|
| 状态 | 客户端过滤 | 标签页：全部 / 成功 / 失败 / 进行中 |
| 模型 | 客户端过滤 | 下拉选择，从已加载数据提取去重模型列表 |

**"进行中"合并：** accepted + queued + running 统一归为"进行中"。

**筛选与分页交互：**
- 筛选只作用于已加载的数据
- 筛选后显示："筛选 12 条 / 已加载 100 条"
- 如果筛选结果少 + 有更多数据，底部提示"可能还有更多，试试加载更多"

**新增 FilterBar 组件：**
```
┌─────────────────────────────────────┐
│ [全部] [成功] [失败] [进行中]         │
│ 模型: [全部 ▼]                      │
│ 筛选 12 条 / 已加载 100 条           │
└─────────────────────────────────────┘
```

### 5.3 任务卡片增加耗时

对 succeeded / failed 任务，在卡片中显示耗时：
- 计算方式：`finished_at - created_at`（秒）
- 显示格式：`< 60s` 显示 `{n}s`，`>= 60s` 显示 `{m}m{s}s`
- 位置：时间标签旁，如 `1小时前 · 50s`

### 5.4 提示文案

列表头部下方增加小字提示：
```
任务记录保持约 3 天 · 图片有效期约 3 小时
```

样式：`text-[10px] text-base-content/30`，不抢视觉焦点。

---

## 六、相册视图

### 6.1 数据加载

```
进入相册视图
  |
  +-- 从已加载的任务列表中筛出 status === "succeeded" 的 ID
  |
  +-- 如果 ID 数 <= 100:
  |   POST /api/v1/tasks/batch-get { "ids": [...] }
  |
  +-- 如果 ID 数 > 100:
  |   分批请求，每批 100 个
  |
  +-- 从响应中提取所有图片 URL
  |   每张图关联元数据：
  |   {
  |     taskId: string,
  |     model: string,
  |     createdAt: number,
  |     imageUrl: string,
  |     imageIndex: number  // 同一任务可能多张图
  |   }
  |
  +-- 渲染瀑布流
```

**API 新增（client.ts）：**
```typescript
interface BatchGetRequest {
  ids: string[];
}

interface BatchGetResponse {
  object: string;
  items: TaskDetailResponse[];
  next_poll_after_ms: number;
}

function batchGetTasks(apiKey: string, ids: string[]): Promise<BatchGetResponse>
```

### 6.2 瀑布流布局

**响应式列数：**

| 屏幕宽度 | 列数 |
|----------|------|
| >= 1280px | 4 列 |
| 1024-1279px | 3 列 |
| 768-1023px | 3 列 |
| < 768px | 2 列 |

**布局实现：** CSS `column-count` + `break-inside: avoid`（纯 CSS 方案，无需额外库）。

**每张图片卡片（GalleryCard）：**
```
┌──────────────┐
│ ☐            │  ← 左上角勾选框（hover 时显示）
│              │
│    图片      │  ← 点击打开灯箱
│              │
│         ⬇   │  ← 右下角下载按钮（hover 时显示）
├──────────────┤
│ svip · 1小时前│  ← 底部信息条
└──────────────┘
```

- hover：勾选框和下载按钮浮现 + 图片轻微变亮
- 点击图片：打开灯箱查看大图
- 点击勾选框：选中/取消（蓝色边框 + 半透明遮罩）
- 点击下载：单张下载

### 6.3 批量操作工具栏（DownloadBar）

选中至少一张图片后，底部浮现工具栏：

```
┌─────────────────────────────────────────────────────┐
│  已选 3 张   [全选]  [取消全选]   [⬇ 打包下载 ZIP]   │
└─────────────────────────────────────────────────────┘
```

- 固定在视口底部，`position: fixed`
- 入场动画：从底部滑入
- 无选中时自动隐藏

### 6.4 相册筛选

与任务视图共享模型下拉筛选。无需状态筛选（相册只展示 succeeded）。

顶部显示统计："共 47 张图片"

### 6.5 空状态

无 succeeded 任务时显示：
```
暂无图片
最近 3 天没有成功生成的图片
```

### 6.6 加载状态

批量加载时显示进度：
```
正在加载图片... (2/3 批)
```

---

## 七、下载功能

### 7.1 单张下载

```
点击 GalleryCard 的 ⬇ 按钮
  |
  +-- fetch(imageUrl) → blob
  |   (跨域图片需 no-cors 模式 → 改用 Worker 代理)
  |
  +-- saveAs(blob, "task-{taskId}-{imageIndex}.png")
  |
  +-- 失败 → toast "图片已过期或不可用，请刷新页面"
```

**跨域处理：** 图床 URL 可能不支持 CORS。方案：
- 在 Worker 中增加 `/api/download?url=xxx` 代理端点
- Worker fetch 图片 → 直接返回 blob 给前端
- 限制只代理已知图床域名（安全）

### 7.2 批量 ZIP 下载

```
点击 "打包下载 ZIP"
  |
  +-- 显示进度 modal："正在打包 0/N..."
  |
  +-- 逐张 fetch blob（通过 Worker 代理）
  |   每完成一张更新进度
  |   失败的跳过，记录数量
  |
  +-- const zip = new JSZip()
  |   zip.file("task-{id}-{index}.png", blob)
  |
  +-- zip.generateAsync({ type: "blob" })
  |
  +-- saveAs(blob, "images-{YYYY-MM-DD-HHmmss}.zip")
  |
  +-- 关闭 modal
  |
  +-- 如果有失败：toast "已下载 N 张，M 张因过期跳过"
```

### 7.3 Worker 下载代理端点

**新增路由：** `GET /api/download?url={encodedUrl}`

**Worker 逻辑：**
```
1. 解析 url 参数
2. 校验域名在 allowlist 内（R2 域名、uguu.se 等）
3. fetch(url) 获取图片
4. 返回图片 response（Content-Type 透传）
5. 不缓存（图片有时效性）
```

---

## 八、新增依赖

| 依赖 | 版本 | 用途 | gzip 大小 |
|------|------|------|----------|
| jszip | ^3.10 | ZIP 打包 | ~100KB |
| file-saver | ^2.0 | 触发浏览器下载 | ~3KB |

构建产物预估增长：~103KB gzip → 总计 ~165KB gzip。

---

## 九、新增/修改文件清单

### 新增文件

```
src/
├── components/
│   ├── GalleryView.tsx       # 相册主视图
│   ├── MasonryGrid.tsx       # 瀑布流容器（CSS column-count）
│   ├── GalleryCard.tsx       # 单张图片卡片
│   ├── DownloadBar.tsx       # 批量下载工具栏
│   ├── FilterBar.tsx         # 筛选条（状态标签页 + 模型下拉）
│   ├── LoadMoreButton.tsx    # 加载更多按钮
│   └── ApiKeyInput.tsx       # API Key 内联编辑
├── hooks/
│   └── useGallery.ts         # 相册数据（batch-get + 图片提取）
└── utils/
    └── download.ts           # 下载工具（单张 + ZIP 打包）
```

### 修改文件

| 文件 | 变更 |
|------|------|
| `worker/api-proxy.ts` | 新增 `/api/download` 代理端点 |
| `src/api/client.ts` | 新增 `batchGetTasks()` 方法 |
| `src/hooks/useTasks.ts` | limit=100, 新增 `loadMore()`, `hasMore` |
| `src/components/TopBar.tsx` | 视图切换 btn-group + ApiKeyInput |
| `src/components/DashboardPage.tsx` | view state 路由 + 筛选状态 + 传递 initialItems 给相册 |
| `src/components/TaskCard.tsx` | 单行扁平布局 + 耗时显示 + 模型名缩写 |
| `src/components/StatusBadge.tsx` | badge-xs 尺寸 |
| `src/components/TaskList.tsx` | 集成 FilterBar + LoadMore + 提示文案 + 间距缩减 |
| `src/components/TaskDetail.tsx` | 详情面板增加单张下载按钮 |
| `src/utils/time.ts` | 新增 `formatDuration(seconds)` 函数 |

---

## 十、安全考量

- **下载代理端点域名白名单**：只允许代理已知图床域名，防止 SSRF
- **API Key 编辑**：内联编辑时输入框为 `type="password"`，避免明文暴露
- **ZIP 内存限制**：前端限制单次打包最多 50 张图片，防止浏览器 OOM

---

## 十一、不做的事（YAGNI）

- 不做图片预加载/懒加载优化（数据量有限，batch-get 一次搞定）
- 不做排序切换（后端默认按时间倒序，够用）
- 不做全选后全部下载按钮（用全选 + 打包下载 ZIP 组合实现）
- 不做图片收藏/标记功能
- 不做任务搜索（筛选已覆盖核心需求）
