# Task Dashboard V2 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 为 Task Dashboard 增加 UI 密度优化、分页/筛选、API Key 编辑、相册视图（瀑布流）、图片下载（单张 + ZIP 打包）。

**Architecture:** 在 V1 基础上改造现有组件（TaskCard/StatusBadge/TaskList/TopBar/DashboardPage）并新增相册模块（GalleryView + 瀑布流 + 下载）。Worker 侧新增 `/api/download` 图片代理端点。视图切换用 state 管理，不引入路由库。

**Tech Stack:** React 18, Tailwind CSS, DaisyUI, JSZip, file-saver, Cloudflare Workers

**设计文档:** `docs/plans/2026-04-12-task-dashboard-v2-design.md`

**项目目录:** `cloudflare/task-dashboard/`（以下所有路径相对于此目录）

---

## 文件结构总览

### 新增文件

```
src/components/FilterBar.tsx       # 状态标签页 + 模型下拉筛选
src/components/LoadMoreButton.tsx  # 加载更多按钮
src/components/ApiKeyInput.tsx     # API Key 内联编辑
src/components/GalleryView.tsx     # 相册主视图
src/components/MasonryGrid.tsx     # 瀑布流容器
src/components/GalleryCard.tsx     # 相册图片卡片
src/components/DownloadBar.tsx     # 批量下载工具栏
src/hooks/useGallery.ts            # 相册数据加载
src/utils/download.ts              # 下载工具（单张 + ZIP）
```

### 修改文件

```
worker/index.ts                    # 新增 /api/download 路由
worker/api-proxy.ts                # 新增 proxyImageDownload 函数
src/api/client.ts                  # 新增 batchGetTasks()，fetchTaskList limit=100 + 分页参数
src/utils/time.ts                  # 新增 formatDuration()
src/utils/status.ts                # 新增 isInProgressStatus()
src/hooks/useTasks.ts              # 支持分页 loadMore + hasMore
src/components/StatusBadge.tsx     # badge-xs 尺寸
src/components/TaskCard.tsx        # 增加耗时显示
src/components/TaskList.tsx        # 集成 FilterBar + LoadMore + 提示文案 + 紧凑布局
src/components/TopBar.tsx          # 视图切换 + ApiKeyInput
src/components/DashboardPage.tsx   # view 路由 + 筛选状态 + 相册集成
src/components/TaskDetail.tsx      # 增加单张下载按钮
src/App.tsx                        # onChangeApiKey 回调
src/index.css                      # 瀑布流 CSS
package.json                       # 新增 jszip, file-saver
```

---

### Task 1: 工具层 — formatDuration + API 分页 + batchGet

**Files:**
- Modify: `src/utils/time.ts`
- Modify: `src/utils/status.ts`
- Modify: `src/api/client.ts`

- [ ] **Step 1: 在 time.ts 末尾新增 formatDuration**

```typescript
/** Format duration in seconds, e.g. 50 → "50s", 125 → "2m5s" */
export function formatDuration(seconds: number): string {
  if (seconds < 0) return "";
  if (seconds < 60) return `${Math.round(seconds)}s`;
  const m = Math.floor(seconds / 60);
  const s = Math.round(seconds % 60);
  return s > 0 ? `${m}m${s}s` : `${m}m`;
}
```

- [ ] **Step 2: 在 status.ts 末尾新增 isInProgressStatus**

```typescript
export function isInProgressStatus(status: string): boolean {
  return status === "accepted" || status === "queued" || status === "running";
}
```

- [ ] **Step 3: 修改 client.ts — fetchTaskList 支持分页参数，limit 改为 100**

将 `fetchTaskList` 替换为：

```typescript
export interface FetchTaskListOptions {
  limit?: number;
  beforeCreatedAt?: number;
  beforeId?: string;
}

export function fetchTaskList(
  apiKey: string,
  options?: FetchTaskListOptions
): Promise<TaskListResponse> {
  const limit = options?.limit ?? 100;
  let path = `/api/v1/tasks?limit=${limit}`;
  if (options?.beforeCreatedAt && options?.beforeId) {
    path += `&before_created_at=${options.beforeCreatedAt}&before_id=${encodeURIComponent(options.beforeId)}`;
  }
  return request<TaskListResponse>(path, apiKey);
}
```

- [ ] **Step 4: 在 client.ts 末尾新增 batchGetTasks**

```typescript
export interface BatchGetResponse {
  object: string;
  items: TaskDetailResponse[];
  next_poll_after_ms: number;
}

export function batchGetTasks(
  apiKey: string,
  ids: string[]
): Promise<BatchGetResponse> {
  return request<BatchGetResponse>("/api/v1/tasks/batch-get", apiKey, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ids }),
  });
}
```

- [ ] **Step 5: 验证编译**

```bash
cd cloudflare/task-dashboard && npx tsc --noEmit
```

- [ ] **Step 6: Commit**

```bash
git add src/utils/time.ts src/utils/status.ts src/api/client.ts
git commit -m "feat(task-dashboard): add formatDuration, pagination params, and batchGetTasks API"
```

---

### Task 2: UI 密度优化 — StatusBadge + TaskCard + TaskList

**Files:**
- Modify: `src/components/StatusBadge.tsx`
- Modify: `src/components/TaskCard.tsx`
- Modify: `src/components/TaskList.tsx`

- [ ] **Step 1: StatusBadge 改为 badge-xs**

完整替换 `src/components/StatusBadge.tsx`：

```tsx
import { getStatusConfig } from "../utils/status";

interface StatusBadgeProps {
  status: string;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const config = getStatusConfig(status);

  return (
    <span
      className={`badge badge-xs text-[10px] px-1.5 ${config.badgeClass} ${
        config.animate ? "animate-status-pulse" : ""
      }`}
    >
      {config.label}
    </span>
  );
}
```

- [ ] **Step 2: TaskCard 增加耗时显示**

完整替换 `src/components/TaskCard.tsx`：

```tsx
import { StatusBadge } from "./StatusBadge";
import { formatTime, timeAgo, formatDuration } from "../utils/time";
import type { TaskListItem } from "../api/client";

interface TaskCardProps {
  task: TaskListItem;
  isSelected: boolean;
  index: number;
  onClick: () => void;
}

export function TaskCard({ task, isSelected, index, onClick }: TaskCardProps) {
  const shortModel = task.model
    .replace("gemini-3-pro-image-preview-", "")
    .replace("gemini-3-pro-image-preview", "imagen");

  const duration =
    task.finished_at && task.created_at
      ? formatDuration(task.finished_at - task.created_at)
      : null;

  return (
    <div
      className={`flex items-center gap-2 px-2.5 py-1.5 rounded-md cursor-pointer border transition-all duration-150 animate-card-enter ${
        isSelected
          ? "border-primary/60 bg-primary/10"
          : "border-transparent hover:bg-base-200/60"
      }`}
      style={{ animationDelay: `${Math.min(index, 15) * 30}ms` }}
      onClick={onClick}
    >
      <span className="text-[11px] font-medium text-base-content/70 bg-base-200 px-1.5 py-0.5 rounded shrink-0">
        {shortModel}
      </span>

      <span className="font-mono text-[11px] text-base-content/40 truncate flex-1 min-w-0">
        {task.id}
      </span>

      <span
        className="text-[10px] text-base-content/35 shrink-0"
        title={formatTime(task.created_at)}
      >
        {timeAgo(task.created_at)}
        {duration && <span className="text-base-content/25"> · {duration}</span>}
      </span>

      <StatusBadge status={task.status} />
    </div>
  );
}
```

- [ ] **Step 3: TaskList 紧凑布局 + 提示文案**

完整替换 `src/components/TaskList.tsx`：

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
      {/* Compact header */}
      <div className="px-3 py-2 border-b border-base-300">
        <div className="flex items-center justify-between">
          <span className="text-xs text-base-content/50">
            最近 3 天 · {items.length} 个任务
          </span>
          <button
            className="btn btn-ghost btn-circle btn-xs"
            onClick={onRefresh}
            disabled={loading}
            title="刷新"
          >
            <svg
              xmlns="http://www.w3.org/2000/svg"
              className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`}
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
        <p className="text-[10px] text-base-content/30 mt-0.5">
          任务记录保持约 3 天 · 图片有效期约 3 小时
        </p>
      </div>

      {error && (
        <div className="alert alert-error mx-3 mt-2 py-1.5 text-xs">
          <span>{error}</span>
          <button className="btn btn-ghost btn-xs" onClick={onRefresh}>
            重试
          </button>
        </div>
      )}

      <div className="flex-1 overflow-y-auto px-1.5 py-1 space-y-0.5">
        {!loading && items.length === 0 && !error && (
          <EmptyState icon="list" title="暂无任务" description="最近 3 天没有生图任务" />
        )}

        {loading && items.length === 0 && (
          <div className="flex items-center justify-center py-16">
            <span className="loading loading-spinner loading-sm" />
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

- [ ] **Step 4: 验证构建**

```bash
cd cloudflare/task-dashboard && npm run build
```

- [ ] **Step 5: Commit**

```bash
git add src/components/StatusBadge.tsx src/components/TaskCard.tsx src/components/TaskList.tsx
git commit -m "feat(task-dashboard): compact ui density - single-line cards, smaller badges, hints"
```

---

### Task 3: 筛选栏 + 加载更多 + useTasks 分页

**Files:**
- Create: `src/components/FilterBar.tsx`
- Create: `src/components/LoadMoreButton.tsx`
- Modify: `src/hooks/useTasks.ts`

- [ ] **Step 1: 创建 FilterBar**

```tsx
// src/components/FilterBar.tsx
import type { TaskListItem } from "../api/client";
import { isInProgressStatus } from "../utils/status";

export type StatusFilter = "all" | "succeeded" | "failed" | "in_progress";

interface FilterBarProps {
  items: TaskListItem[];
  filteredCount: number;
  statusFilter: StatusFilter;
  modelFilter: string;
  onStatusFilterChange: (filter: StatusFilter) => void;
  onModelFilterChange: (model: string) => void;
}

const STATUS_TABS: { value: StatusFilter; label: string }[] = [
  { value: "all", label: "全部" },
  { value: "succeeded", label: "成功" },
  { value: "failed", label: "失败" },
  { value: "in_progress", label: "进行中" },
];

export function FilterBar({
  items,
  filteredCount,
  statusFilter,
  modelFilter,
  onStatusFilterChange,
  onModelFilterChange,
}: FilterBarProps) {
  // Extract unique models from loaded data
  const models = Array.from(new Set(items.map((t) => t.model))).sort();

  return (
    <div className="px-3 py-1.5 border-b border-base-300 space-y-1">
      {/* Status tabs */}
      <div className="flex gap-1">
        {STATUS_TABS.map((tab) => (
          <button
            key={tab.value}
            className={`btn btn-xs ${
              statusFilter === tab.value ? "btn-primary" : "btn-ghost"
            }`}
            onClick={() => onStatusFilterChange(tab.value)}
          >
            {tab.label}
          </button>
        ))}
      </div>

      <div className="flex items-center justify-between">
        {/* Model select */}
        <select
          className="select select-xs select-bordered w-auto max-w-[180px] text-[11px]"
          value={modelFilter}
          onChange={(e) => onModelFilterChange(e.target.value)}
        >
          <option value="">全部模型</option>
          {models.map((m) => (
            <option key={m} value={m}>
              {m.replace("gemini-3-pro-image-preview-", "").replace("gemini-3-pro-image-preview", "imagen")}
            </option>
          ))}
        </select>

        {/* Count */}
        <span className="text-[10px] text-base-content/30">
          {statusFilter !== "all" || modelFilter
            ? `筛选 ${filteredCount} 条 / 已加载 ${items.length} 条`
            : `${items.length} 条`}
        </span>
      </div>
    </div>
  );
}

/** Apply client-side filters to task items */
export function applyFilters(
  items: TaskListItem[],
  statusFilter: StatusFilter,
  modelFilter: string
): TaskListItem[] {
  let filtered = items;

  if (statusFilter === "succeeded") {
    filtered = filtered.filter((t) => t.status === "succeeded");
  } else if (statusFilter === "failed") {
    filtered = filtered.filter((t) => t.status === "failed");
  } else if (statusFilter === "in_progress") {
    filtered = filtered.filter((t) => isInProgressStatus(t.status));
  }

  if (modelFilter) {
    filtered = filtered.filter((t) => t.model === modelFilter);
  }

  return filtered;
}
```

- [ ] **Step 2: 创建 LoadMoreButton**

```tsx
// src/components/LoadMoreButton.tsx
interface LoadMoreButtonProps {
  hasMore: boolean;
  loading: boolean;
  onClick: () => void;
}

export function LoadMoreButton({ hasMore, loading, onClick }: LoadMoreButtonProps) {
  if (!hasMore) {
    return (
      <p className="text-center text-[10px] text-base-content/25 py-2">
        已加载全部
      </p>
    );
  }

  return (
    <div className="flex justify-center py-2">
      <button
        className="btn btn-ghost btn-xs text-[11px]"
        onClick={onClick}
        disabled={loading}
      >
        {loading ? (
          <span className="loading loading-spinner loading-xs" />
        ) : (
          "加载更多"
        )}
      </button>
    </div>
  );
}
```

- [ ] **Step 3: 改造 useTasks — 分页支持**

完整替换 `src/hooks/useTasks.ts`：

```typescript
import { useState, useCallback } from "react";
import {
  fetchTaskList,
  ApiError,
  type TaskListItem,
} from "../api/client";

const PAGE_SIZE = 100;

interface UseTasksState {
  items: TaskListItem[];
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  error: string | null;
}

export function useTasks(apiKey: string | null, initialItems?: TaskListItem[] | null) {
  const [state, setState] = useState<UseTasksState>({
    items: initialItems ?? [],
    loading: false,
    loadingMore: false,
    hasMore: initialItems ? initialItems.length >= PAGE_SIZE : true,
    error: null,
  });

  /** Full reload — replaces item list */
  const load = useCallback(async () => {
    if (!apiKey) return;

    setState((prev) => ({ ...prev, loading: true, error: null }));

    try {
      const data = await fetchTaskList(apiKey, { limit: PAGE_SIZE });
      setState({
        items: data.items,
        loading: false,
        loadingMore: false,
        hasMore: data.items.length >= PAGE_SIZE,
        error: null,
      });
    } catch (err) {
      const message =
        err instanceof ApiError
          ? err.message
          : err instanceof TypeError
            ? "网络连接失败，请检查网络"
            : "加载失败";
      setState((prev) => ({ ...prev, loading: false, error: message }));

      if (err instanceof ApiError && err.status === 401) {
        throw err;
      }
    }
  }, [apiKey]);

  /** Append next page */
  const loadMore = useCallback(async () => {
    if (!apiKey || state.items.length === 0) return;

    setState((prev) => ({ ...prev, loadingMore: true, error: null }));

    const lastItem = state.items[state.items.length - 1];

    try {
      const data = await fetchTaskList(apiKey, {
        limit: PAGE_SIZE,
        beforeCreatedAt: lastItem.created_at,
        beforeId: lastItem.id,
      });
      setState((prev) => ({
        ...prev,
        items: [...prev.items, ...data.items],
        loadingMore: false,
        hasMore: data.items.length >= PAGE_SIZE,
      }));
    } catch (err) {
      const message =
        err instanceof ApiError ? err.message : "加载更多失败";
      setState((prev) => ({ ...prev, loadingMore: false, error: message }));
    }
  }, [apiKey, state.items]);

  return { ...state, load, loadMore };
}
```

- [ ] **Step 4: 验证编译**

```bash
cd cloudflare/task-dashboard && npx tsc --noEmit
```

- [ ] **Step 5: Commit**

```bash
git add src/components/FilterBar.tsx src/components/LoadMoreButton.tsx src/hooks/useTasks.ts
git commit -m "feat(task-dashboard): add filter bar, load more button, and pagination support"
```

---

### Task 4: ApiKeyInput + TopBar 改造

**Files:**
- Create: `src/components/ApiKeyInput.tsx`
- Modify: `src/components/TopBar.tsx`

- [ ] **Step 1: 创建 ApiKeyInput**

```tsx
// src/components/ApiKeyInput.tsx
import { useState, useRef, useEffect, type FormEvent } from "react";
import { fetchTaskList, ApiError } from "../api/client";

interface ApiKeyInputProps {
  currentKey: string;
  onChangeKey: (newKey: string) => void;
}

export function ApiKeyInput({ currentKey, onChangeKey }: ApiKeyInputProps) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
    }
  }, [editing]);

  const masked = currentKey
    ? `sk-****${currentKey.slice(-4)}`
    : "未设置";

  function startEdit() {
    setValue("");
    setError(null);
    setEditing(true);
  }

  function cancelEdit() {
    setEditing(false);
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;

    setLoading(true);
    setError(null);

    try {
      await fetchTaskList(trimmed, { limit: 1 });
      onChangeKey(trimmed);
      setEditing(false);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError("Key 无效");
      } else {
        setError("验证失败");
      }
    } finally {
      setLoading(false);
    }
  }

  if (!editing) {
    return (
      <button
        className="btn btn-ghost btn-xs text-[11px] font-mono gap-1"
        onClick={startEdit}
        title="更换 API Key"
      >
        {masked}
        <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z" />
        </svg>
      </button>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex items-center gap-1">
      <input
        ref={inputRef}
        type="password"
        className={`input input-xs input-bordered w-40 text-[11px] ${error ? "input-error" : ""}`}
        placeholder="输入新 API Key"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && cancelEdit()}
        disabled={loading}
      />
      <button
        type="submit"
        className="btn btn-xs btn-primary"
        disabled={loading || !value.trim()}
      >
        {loading ? <span className="loading loading-spinner loading-xs" /> : "确认"}
      </button>
      <button
        type="button"
        className="btn btn-xs btn-ghost"
        onClick={cancelEdit}
        disabled={loading}
      >
        取消
      </button>
      {error && <span className="text-[10px] text-error">{error}</span>}
    </form>
  );
}
```

- [ ] **Step 2: 改造 TopBar — 增加视图切换 + ApiKeyInput**

完整替换 `src/components/TopBar.tsx`：

```tsx
import { ApiKeyInput } from "./ApiKeyInput";

export type ViewMode = "tasks" | "gallery";

interface TopBarProps {
  theme: "light" | "dark";
  view: ViewMode;
  apiKey: string;
  onToggleTheme: () => void;
  onViewChange: (view: ViewMode) => void;
  onChangeApiKey: (newKey: string) => void;
  onLogout: () => void;
}

export function TopBar({
  theme,
  view,
  apiKey,
  onToggleTheme,
  onViewChange,
  onChangeApiKey,
  onLogout,
}: TopBarProps) {
  return (
    <div className="navbar bg-base-100 border-b border-base-300 px-4 min-h-0 h-11">
      {/* Title */}
      <div className="flex-none mr-3">
        <span className="text-sm font-bold">Task Dashboard</span>
      </div>

      {/* View switch */}
      <div className="flex-none">
        <div className="join">
          <button
            className={`join-item btn btn-xs ${view === "tasks" ? "btn-active" : ""}`}
            onClick={() => onViewChange("tasks")}
          >
            任务
          </button>
          <button
            className={`join-item btn btn-xs ${view === "gallery" ? "btn-active" : ""}`}
            onClick={() => onViewChange("gallery")}
          >
            相册
          </button>
        </div>
      </div>

      {/* Spacer */}
      <div className="flex-1" />

      {/* API Key */}
      <div className="flex-none mr-2">
        <ApiKeyInput currentKey={apiKey} onChangeKey={onChangeApiKey} />
      </div>

      {/* Theme toggle */}
      <div className="flex-none">
        <label className="swap swap-rotate btn btn-ghost btn-circle btn-xs">
          <input
            type="checkbox"
            checked={theme === "light"}
            onChange={onToggleTheme}
          />
          <svg className="swap-on fill-current w-4 h-4" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
            <path d="M5.64,17l-.71.71a1,1,0,0,0,0,1.41,1,1,0,0,0,1.41,0l.71-.71A1,1,0,0,0,5.64,17ZM5,12a1,1,0,0,0-1-1H3a1,1,0,0,0,0,2H4A1,1,0,0,0,5,12Zm7-7a1,1,0,0,0,1-1V3a1,1,0,0,0-2,0V4A1,1,0,0,0,12,5ZM5.64,7.05a1,1,0,0,0,.7.29,1,1,0,0,0,.71-.29,1,1,0,0,0,0-1.41l-.71-.71A1,1,0,0,0,4.93,6.34Zm12,.29a1,1,0,0,0,.7-.29l.71-.71a1,1,0,1,0-1.41-1.41L17,5.64a1,1,0,0,0,0,1.41A1,1,0,0,0,17.66,7.34ZM21,11H20a1,1,0,0,0,0,2h1a1,1,0,0,0,0-2Zm-9,8a1,1,0,0,0-1,1v1a1,1,0,0,0,2,0V20A1,1,0,0,0,12,19ZM18.36,17A1,1,0,0,0,17,18.36l.71.71a1,1,0,0,0,1.41,0,1,1,0,0,0,0-1.41ZM12,6.5A5.5,5.5,0,1,0,17.5,12,5.51,5.51,0,0,0,12,6.5Zm0,9A3.5,3.5,0,1,1,15.5,12,3.5,3.5,0,0,1,12,15.5Z" />
          </svg>
          <svg className="swap-off fill-current w-4 h-4" xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">
            <path d="M21.64,13a1,1,0,0,0-1.05-.14,8.05,8.05,0,0,1-3.37.73A8.15,8.15,0,0,1,9.08,5.49a8.59,8.59,0,0,1,.25-2A1,1,0,0,0,8,2.36,10.14,10.14,0,1,0,22,14.05,1,1,0,0,0,21.64,13Zm-9.5,6.69A8.14,8.14,0,0,1,7.08,5.22v.27A10.15,10.15,0,0,0,17.22,15.63a9.79,9.79,0,0,0,2.1-.22A8.11,8.11,0,0,1,12.14,19.73Z" />
          </svg>
        </label>
      </div>

      {/* Logout */}
      <div className="flex-none ml-1">
        <button className="btn btn-ghost btn-xs text-error" onClick={onLogout}>
          退出
        </button>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: 验证编译**

```bash
cd cloudflare/task-dashboard && npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```bash
git add src/components/ApiKeyInput.tsx src/components/TopBar.tsx
git commit -m "feat(task-dashboard): add api key inline editor, view switcher, compact topbar"
```

---

### Task 5: DashboardPage 改造 — 视图路由 + 筛选集成 + API Key 变更

**Files:**
- Modify: `src/components/DashboardPage.tsx`
- Modify: `src/components/TaskList.tsx` (集成 FilterBar + LoadMore)
- Modify: `src/App.tsx`

- [ ] **Step 1: TaskList 集成 FilterBar + LoadMore**

完整替换 `src/components/TaskList.tsx`：

```tsx
import { TaskCard } from "./TaskCard";
import { EmptyState } from "./EmptyState";
import { FilterBar, applyFilters, type StatusFilter } from "./FilterBar";
import { LoadMoreButton } from "./LoadMoreButton";
import type { TaskListItem } from "../api/client";

interface TaskListProps {
  items: TaskListItem[];
  selectedId: string | null;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  error: string | null;
  statusFilter: StatusFilter;
  modelFilter: string;
  onStatusFilterChange: (filter: StatusFilter) => void;
  onModelFilterChange: (model: string) => void;
  onSelect: (taskId: string) => void;
  onRefresh: () => void;
  onLoadMore: () => void;
}

export function TaskList({
  items,
  selectedId,
  loading,
  loadingMore,
  hasMore,
  error,
  statusFilter,
  modelFilter,
  onStatusFilterChange,
  onModelFilterChange,
  onSelect,
  onRefresh,
  onLoadMore,
}: TaskListProps) {
  const filtered = applyFilters(items, statusFilter, modelFilter);

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="px-3 py-2 border-b border-base-300">
        <div className="flex items-center justify-between">
          <span className="text-xs text-base-content/50">
            最近 3 天 · {items.length} 个任务
          </span>
          <button
            className="btn btn-ghost btn-circle btn-xs"
            onClick={onRefresh}
            disabled={loading}
            title="刷新"
          >
            <svg
              xmlns="http://www.w3.org/2000/svg"
              className={`h-3.5 w-3.5 ${loading ? "animate-spin" : ""}`}
              fill="none"
              viewBox="0 0 24 24"
              stroke="currentColor"
            >
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2}
                d="M4 4v5h.582m15.356 2A8.001 8.001 0 004.582 9m0 0H9m11 11v-5h-.581m0 0a8.003 8.003 0 01-15.357-2m15.357 2H15" />
            </svg>
          </button>
        </div>
        <p className="text-[10px] text-base-content/30 mt-0.5">
          任务记录保持约 3 天 · 图片有效期约 3 小时
        </p>
      </div>

      {/* Filters */}
      <FilterBar
        items={items}
        filteredCount={filtered.length}
        statusFilter={statusFilter}
        modelFilter={modelFilter}
        onStatusFilterChange={onStatusFilterChange}
        onModelFilterChange={onModelFilterChange}
      />

      {/* Error */}
      {error && (
        <div className="alert alert-error mx-3 mt-2 py-1.5 text-xs">
          <span>{error}</span>
          <button className="btn btn-ghost btn-xs" onClick={onRefresh}>重试</button>
        </div>
      )}

      {/* List */}
      <div className="flex-1 overflow-y-auto px-1.5 py-1 space-y-0.5">
        {!loading && filtered.length === 0 && !error && (
          <EmptyState icon="list" title="暂无任务" description={
            statusFilter !== "all" || modelFilter
              ? "当前筛选条件下没有任务"
              : "最近 3 天没有生图任务"
          } />
        )}

        {loading && items.length === 0 && (
          <div className="flex items-center justify-center py-16">
            <span className="loading loading-spinner loading-sm" />
          </div>
        )}

        {filtered.map((task, index) => (
          <TaskCard
            key={task.id}
            task={task}
            isSelected={task.id === selectedId}
            index={index}
            onClick={() => onSelect(task.id)}
          />
        ))}

        {/* Load more */}
        {items.length > 0 && (
          <LoadMoreButton
            hasMore={hasMore}
            loading={loadingMore}
            onClick={onLoadMore}
          />
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 2: 改造 DashboardPage — 视图路由 + 筛选状态**

完整替换 `src/components/DashboardPage.tsx`：

```tsx
import { useState, useEffect, useCallback } from "react";
import { TopBar, type ViewMode } from "./TopBar";
import { TaskList } from "./TaskList";
import { TaskDetail } from "./TaskDetail";
import { useTasks } from "../hooks/useTasks";
import { useTaskDetail } from "../hooks/useTaskDetail";
import { useTheme } from "../hooks/useTheme";
import { ApiError, type TaskListItem } from "../api/client";
import type { StatusFilter } from "./FilterBar";

interface DashboardPageProps {
  apiKey: string;
  initialItems: TaskListItem[] | null;
  onLogout: () => void;
  onChangeApiKey: (newKey: string) => void;
}

export function DashboardPage({ apiKey, initialItems, onLogout, onChangeApiKey }: DashboardPageProps) {
  const { theme, toggleTheme } = useTheme();
  const tasks = useTasks(apiKey, initialItems);
  const taskDetail = useTaskDetail(apiKey);
  const [view, setView] = useState<ViewMode>("tasks");
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [showDetail, setShowDetail] = useState(false);
  const [statusFilter, setStatusFilter] = useState<StatusFilter>("all");
  const [modelFilter, setModelFilter] = useState("");

  useEffect(() => {
    if (initialItems && initialItems.length >= 0) return;
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) onLogout();
    });
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const handleRefresh = useCallback(() => {
    tasks.load().catch((err) => {
      if (err instanceof ApiError && err.status === 401) onLogout();
    });
  }, [tasks.load, onLogout]);

  const handleLoadMore = useCallback(() => {
    tasks.loadMore();
  }, [tasks.loadMore]);

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
    if (selectedId) taskDetail.load(selectedId);
  }, [selectedId, taskDetail.load]);

  return (
    <div className="h-screen flex flex-col bg-base-200">
      <TopBar
        theme={theme}
        view={view}
        apiKey={apiKey}
        onToggleTheme={toggleTheme}
        onViewChange={setView}
        onChangeApiKey={onChangeApiKey}
        onLogout={onLogout}
      />

      {view === "tasks" ? (
        <div className="flex-1 flex overflow-hidden">
          <div
            className={`w-full lg:w-2/5 xl:w-[40%] border-r border-base-300 bg-base-100 flex-shrink-0 ${
              showDetail ? "hidden lg:flex lg:flex-col" : "flex flex-col"
            }`}
          >
            <TaskList
              items={tasks.items}
              selectedId={selectedId}
              loading={tasks.loading}
              loadingMore={tasks.loadingMore}
              hasMore={tasks.hasMore}
              error={tasks.error}
              statusFilter={statusFilter}
              modelFilter={modelFilter}
              onStatusFilterChange={setStatusFilter}
              onModelFilterChange={setModelFilter}
              onSelect={handleSelect}
              onRefresh={handleRefresh}
              onLoadMore={handleLoadMore}
            />
          </div>
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
      ) : (
        <div className="flex-1 overflow-y-auto bg-base-100 p-4">
          <div className="text-center text-base-content/40 py-16">
            相册视图（Task 8 实现）
          </div>
        </div>
      )}
    </div>
  );
}
```

- [ ] **Step 3: 改造 App.tsx — 支持 onChangeApiKey**

完整替换 `src/App.tsx`：

```tsx
import { useState } from "react";
import { useAuth } from "./hooks/useAuth";
import { LoginPage } from "./components/LoginPage";
import { DashboardPage } from "./components/DashboardPage";
import type { TaskListItem } from "./api/client";

export function App() {
  const { apiKey, isAuthenticated, login, logout } = useAuth();
  const [initialItems, setInitialItems] = useState<TaskListItem[] | null>(null);

  function handleLogin(key: string, items: TaskListItem[]) {
    login(key);
    setInitialItems(items);
  }

  function handleLogout() {
    logout();
    setInitialItems(null);
  }

  function handleChangeApiKey(newKey: string) {
    login(newKey);
    setInitialItems(null); // Force reload with new key
  }

  if (!isAuthenticated || !apiKey) {
    return <LoginPage onLogin={handleLogin} />;
  }

  return (
    <DashboardPage
      apiKey={apiKey}
      initialItems={initialItems}
      onLogout={handleLogout}
      onChangeApiKey={handleChangeApiKey}
    />
  );
}
```

- [ ] **Step 4: 验证构建**

```bash
cd cloudflare/task-dashboard && npm run build
```

- [ ] **Step 5: Commit**

```bash
git add src/components/DashboardPage.tsx src/components/TaskList.tsx src/App.tsx
git commit -m "feat(task-dashboard): integrate filters, pagination, view routing, api key change"
```

---

### Task 6: Worker 下载代理端点

**Files:**
- Modify: `worker/api-proxy.ts`
- Modify: `worker/index.ts`

- [ ] **Step 1: 在 api-proxy.ts 末尾新增 proxyImageDownload 函数**

```typescript
const ALLOWED_DOWNLOAD_DOMAINS = [
  "pub-",              // R2 public bucket URLs
  "r2.dev",
  "uguu.se",
  "catbox.moe",
  "img.xinbao",
  "xinbaoimage",
];

function isDomainAllowed(url: string): boolean {
  try {
    const hostname = new URL(url).hostname;
    return ALLOWED_DOWNLOAD_DOMAINS.some(
      (d) => hostname.includes(d)
    );
  } catch {
    return false;
  }
}

export async function proxyImageDownload(
  request: Request
): Promise<Response> {
  const url = new URL(request.url);
  const imageUrl = url.searchParams.get("url");

  if (!imageUrl) {
    return new Response(JSON.stringify({ error: "missing url parameter" }), {
      status: 400,
      headers: { "Content-Type": "application/json" },
    });
  }

  if (!isDomainAllowed(imageUrl)) {
    return new Response(JSON.stringify({ error: "domain not allowed" }), {
      status: 403,
      headers: { "Content-Type": "application/json" },
    });
  }

  try {
    const response = await fetch(imageUrl);
    if (!response.ok) {
      return new Response(JSON.stringify({ error: `upstream ${response.status}` }), {
        status: response.status,
        headers: { "Content-Type": "application/json" },
      });
    }

    const headers = new Headers();
    const contentType = response.headers.get("Content-Type");
    if (contentType) headers.set("Content-Type", contentType);
    headers.set("Cache-Control", "no-store");

    return new Response(response.body, { status: 200, headers });
  } catch {
    return new Response(JSON.stringify({ error: "fetch failed" }), {
      status: 502,
      headers: { "Content-Type": "application/json" },
    });
  }
}
```

- [ ] **Step 2: 修改 worker/index.ts — 增加 /api/download 路由**

完整替换 `worker/index.ts`：

```typescript
import { serveStaticAsset, type StaticEnv } from "./static";
import { proxyApiRequest, proxyImageDownload, type ProxyEnv } from "./api-proxy";

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

    // Image download proxy
    if (url.pathname === "/api/download" && request.method === "GET") {
      return proxyImageDownload(request);
    }

    // API proxy: /api/v1/*
    if (url.pathname.startsWith("/api/v1/")) {
      return proxyApiRequest(request, env);
    }

    // Static assets / SPA fallback
    return serveStaticAsset(request, env, ctx);
  },
};
```

- [ ] **Step 3: 验证构建**

```bash
cd cloudflare/task-dashboard && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add worker/api-proxy.ts worker/index.ts
git commit -m "feat(task-dashboard): add /api/download image proxy endpoint with domain allowlist"
```

---

### Task 7: 下载工具 + 依赖安装

**Files:**
- Create: `src/utils/download.ts`
- Modify: `package.json`

- [ ] **Step 1: 安装依赖**

```bash
cd cloudflare/task-dashboard && npm install jszip file-saver && npm install -D @types/file-saver
```

- [ ] **Step 2: 创建 download.ts**

```typescript
// src/utils/download.ts
import JSZip from "jszip";
import { saveAs } from "file-saver";

/** Download a single image via the Worker proxy */
export async function downloadSingleImage(
  imageUrl: string,
  filename: string
): Promise<void> {
  const proxyUrl = `/api/download?url=${encodeURIComponent(imageUrl)}`;
  const response = await fetch(proxyUrl);

  if (!response.ok) {
    throw new Error("图片已过期或不可用");
  }

  const blob = await response.blob();
  saveAs(blob, filename);
}

export interface DownloadItem {
  url: string;
  filename: string;
}

export interface ZipProgress {
  completed: number;
  total: number;
  failed: number;
}

/** Download multiple images and pack into a ZIP file */
export async function downloadAsZip(
  items: DownloadItem[],
  onProgress?: (progress: ZipProgress) => void
): Promise<{ downloaded: number; failed: number }> {
  const zip = new JSZip();
  let completed = 0;
  let failed = 0;

  for (const item of items) {
    try {
      const proxyUrl = `/api/download?url=${encodeURIComponent(item.url)}`;
      const response = await fetch(proxyUrl);

      if (!response.ok) {
        failed++;
      } else {
        const blob = await response.blob();
        zip.file(item.filename, blob);
      }
    } catch {
      failed++;
    }

    completed++;
    onProgress?.({ completed, total: items.length, failed });
  }

  const downloaded = completed - failed;

  if (downloaded > 0) {
    const zipBlob = await zip.generateAsync({ type: "blob" });
    const timestamp = new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-");
    saveAs(zipBlob, `images-${timestamp}.zip`);
  }

  return { downloaded, failed };
}
```

- [ ] **Step 3: 验证编译**

```bash
cd cloudflare/task-dashboard && npx tsc --noEmit
```

- [ ] **Step 4: Commit**

```bash
git add src/utils/download.ts package.json package-lock.json
git commit -m "feat(task-dashboard): add download utils with single and zip batch support"
```

---

### Task 8: 相册 hook + 组件 + GalleryView 组装

**Files:**
- Create: `src/hooks/useGallery.ts`
- Create: `src/components/GalleryCard.tsx`
- Create: `src/components/MasonryGrid.tsx`
- Create: `src/components/DownloadBar.tsx`
- Create: `src/components/GalleryView.tsx`
- Modify: `src/index.css`

- [ ] **Step 1: 创建 useGallery hook**

```typescript
// src/hooks/useGallery.ts
import { useState, useCallback } from "react";
import {
  batchGetTasks,
  extractImageURLs,
  type TaskListItem,
  type TaskDetailResponse,
} from "../api/client";

export interface GalleryImage {
  taskId: string;
  model: string;
  createdAt: number;
  imageUrl: string;
  imageIndex: number;
}

interface UseGalleryState {
  images: GalleryImage[];
  loading: boolean;
  error: string | null;
  progress: { current: number; total: number } | null;
}

export function useGallery(apiKey: string | null) {
  const [state, setState] = useState<UseGalleryState>({
    images: [],
    loading: false,
    error: null,
    progress: null,
  });

  const load = useCallback(
    async (taskItems: TaskListItem[]) => {
      if (!apiKey) return;

      const succeededIds = taskItems
        .filter((t) => t.status === "succeeded")
        .map((t) => t.id);

      if (succeededIds.length === 0) {
        setState({ images: [], loading: false, error: null, progress: null });
        return;
      }

      setState({ images: [], loading: true, error: null, progress: null });

      try {
        // Split into batches of 100
        const batches: string[][] = [];
        for (let i = 0; i < succeededIds.length; i += 100) {
          batches.push(succeededIds.slice(i, i + 100));
        }

        const allImages: GalleryImage[] = [];

        for (let i = 0; i < batches.length; i++) {
          setState((prev) => ({
            ...prev,
            progress: { current: i + 1, total: batches.length },
          }));

          const response = await batchGetTasks(apiKey, batches[i]);

          for (const item of response.items) {
            const detail = item as TaskDetailResponse;
            if (detail.status !== "succeeded") continue;

            const urls = extractImageURLs(detail);
            for (let idx = 0; idx < urls.length; idx++) {
              allImages.push({
                taskId: detail.id,
                model: detail.model,
                createdAt: detail.created_at,
                imageUrl: urls[idx],
                imageIndex: idx,
              });
            }
          }
        }

        setState({
          images: allImages,
          loading: false,
          error: null,
          progress: null,
        });
      } catch (err) {
        setState({
          images: [],
          loading: false,
          error: err instanceof Error ? err.message : "加载失败",
          progress: null,
        });
      }
    },
    [apiKey]
  );

  return { ...state, load };
}
```

- [ ] **Step 2: 创建 GalleryCard**

```tsx
// src/components/GalleryCard.tsx
import { useState } from "react";
import { timeAgo } from "../utils/time";
import type { GalleryImage } from "../hooks/useGallery";

interface GalleryCardProps {
  image: GalleryImage;
  selected: boolean;
  onToggleSelect: () => void;
  onDownload: () => void;
  onPreview: () => void;
}

export function GalleryCard({
  image,
  selected,
  onToggleSelect,
  onDownload,
  onPreview,
}: GalleryCardProps) {
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState(false);

  const shortModel = image.model
    .replace("gemini-3-pro-image-preview-", "")
    .replace("gemini-3-pro-image-preview", "imagen");

  return (
    <div
      className={`group relative rounded-lg overflow-hidden border-2 transition-all duration-150 mb-3 break-inside-avoid ${
        selected ? "border-primary ring-2 ring-primary/30" : "border-transparent"
      }`}
    >
      {/* Checkbox */}
      <label
        className={`absolute top-2 left-2 z-10 cursor-pointer transition-opacity ${
          selected ? "opacity-100" : "opacity-0 group-hover:opacity-100"
        }`}
        onClick={(e) => e.stopPropagation()}
      >
        <input
          type="checkbox"
          className="checkbox checkbox-primary checkbox-xs"
          checked={selected}
          onChange={onToggleSelect}
        />
      </label>

      {/* Download button */}
      <button
        className="absolute bottom-10 right-2 z-10 btn btn-circle btn-xs bg-base-100/80 border-none opacity-0 group-hover:opacity-100 transition-opacity"
        onClick={(e) => {
          e.stopPropagation();
          onDownload();
        }}
        title="下载"
      >
        <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
        </svg>
      </button>

      {/* Image */}
      {!loaded && !error && (
        <div className="aspect-square animate-shimmer bg-base-300" />
      )}
      {error && (
        <div className="aspect-square bg-base-300 flex items-center justify-center">
          <span className="text-[10px] text-base-content/40">已过期</span>
        </div>
      )}
      {!error && (
        <img
          src={image.imageUrl}
          alt=""
          className={`w-full cursor-pointer group-hover:brightness-110 transition-all ${
            loaded ? "opacity-100" : "opacity-0 absolute"
          }`}
          onLoad={() => setLoaded(true)}
          onError={() => setError(true)}
          onClick={onPreview}
        />
      )}

      {/* Info bar */}
      <div className="px-2 py-1 bg-base-200/80 text-[10px] text-base-content/50 flex justify-between">
        <span>{shortModel}</span>
        <span>{timeAgo(image.createdAt)}</span>
      </div>
    </div>
  );
}
```

- [ ] **Step 3: 创建 MasonryGrid**

```tsx
// src/components/MasonryGrid.tsx
import type { ReactNode } from "react";

interface MasonryGridProps {
  children: ReactNode;
}

export function MasonryGrid({ children }: MasonryGridProps) {
  return (
    <div className="masonry-grid">
      {children}
    </div>
  );
}
```

- [ ] **Step 4: 创建 DownloadBar**

```tsx
// src/components/DownloadBar.tsx
interface DownloadBarProps {
  selectedCount: number;
  totalCount: number;
  downloading: boolean;
  downloadProgress: { completed: number; total: number; failed: number } | null;
  onSelectAll: () => void;
  onClearSelection: () => void;
  onDownloadZip: () => void;
}

export function DownloadBar({
  selectedCount,
  totalCount,
  downloading,
  downloadProgress,
  onSelectAll,
  onClearSelection,
  onDownloadZip,
}: DownloadBarProps) {
  if (selectedCount === 0 && !downloading) return null;

  return (
    <div className="fixed bottom-4 left-1/2 -translate-x-1/2 z-50 animate-detail-enter">
      <div className="bg-base-300 shadow-xl rounded-xl px-4 py-2 flex items-center gap-3 border border-base-content/10">
        {downloading && downloadProgress ? (
          <span className="text-xs">
            正在打包 {downloadProgress.completed}/{downloadProgress.total}...
          </span>
        ) : (
          <>
            <span className="text-xs font-medium">已选 {selectedCount} 张</span>
            <button
              className="btn btn-ghost btn-xs"
              onClick={selectedCount < totalCount ? onSelectAll : onClearSelection}
            >
              {selectedCount < totalCount ? "全选" : "取消全选"}
            </button>
            <button className="btn btn-ghost btn-xs" onClick={onClearSelection}>
              清除
            </button>
            <button
              className="btn btn-primary btn-xs gap-1"
              onClick={onDownloadZip}
              disabled={selectedCount === 0}
            >
              <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
                <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
              </svg>
              打包下载 ZIP
            </button>
          </>
        )}
      </div>
    </div>
  );
}
```

- [ ] **Step 5: 创建 GalleryView**

```tsx
// src/components/GalleryView.tsx
import { useEffect, useState, useCallback, useRef } from "react";
import { GalleryCard } from "./GalleryCard";
import { MasonryGrid } from "./MasonryGrid";
import { DownloadBar } from "./DownloadBar";
import { EmptyState } from "./EmptyState";
import { useGallery, type GalleryImage } from "../hooks/useGallery";
import { downloadSingleImage, downloadAsZip, type ZipProgress } from "../utils/download";
import type { TaskListItem } from "../api/client";

interface GalleryViewProps {
  apiKey: string;
  taskItems: TaskListItem[];
  modelFilter: string;
}

export function GalleryView({ apiKey, taskItems, modelFilter }: GalleryViewProps) {
  const gallery = useGallery(apiKey);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [downloading, setDownloading] = useState(false);
  const [downloadProgress, setDownloadProgress] = useState<ZipProgress | null>(null);
  const [lightboxUrl, setLightboxUrl] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const loadedRef = useRef(false);

  // Load gallery data when task items change
  useEffect(() => {
    if (taskItems.length > 0 && !loadedRef.current) {
      loadedRef.current = true;
      gallery.load(taskItems);
    }
  }, [taskItems]); // eslint-disable-line react-hooks/exhaustive-deps

  // Filter by model
  const filteredImages = modelFilter
    ? gallery.images.filter((img) => img.model === modelFilter)
    : gallery.images;

  const imageKey = (img: GalleryImage) => `${img.taskId}-${img.imageIndex}`;

  const toggleSelect = useCallback((img: GalleryImage) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      const key = imageKey(img);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const selectAll = useCallback(() => {
    setSelectedIds(new Set(filteredImages.map(imageKey)));
  }, [filteredImages]);

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set());
  }, []);

  const handleSingleDownload = useCallback(async (img: GalleryImage) => {
    try {
      await downloadSingleImage(
        img.imageUrl,
        `task-${img.taskId}-${img.imageIndex}.png`
      );
    } catch {
      alert("图片已过期或不可用，请刷新页面");
    }
  }, []);

  const handleZipDownload = useCallback(async () => {
    const selected = filteredImages.filter((img) =>
      selectedIds.has(imageKey(img))
    );
    if (selected.length === 0) return;

    setDownloading(true);
    setDownloadProgress(null);

    const items = selected.map((img) => ({
      url: img.imageUrl,
      filename: `task-${img.taskId}-${img.imageIndex}.png`,
    }));

    const result = await downloadAsZip(items, setDownloadProgress);

    setDownloading(false);
    setDownloadProgress(null);
    setSelectedIds(new Set());

    if (result.failed > 0) {
      alert(`已下载 ${result.downloaded} 张，${result.failed} 张因过期跳过`);
    }
  }, [filteredImages, selectedIds]);

  function openLightbox(url: string) {
    setLightboxUrl(url);
    dialogRef.current?.showModal();
  }

  function closeLightbox() {
    dialogRef.current?.close();
    setLightboxUrl(null);
  }

  if (gallery.loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-2">
        <span className="loading loading-spinner loading-md" />
        {gallery.progress && (
          <span className="text-xs text-base-content/40">
            正在加载图片... ({gallery.progress.current}/{gallery.progress.total} 批)
          </span>
        )}
      </div>
    );
  }

  if (gallery.error) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-2">
        <p className="text-error text-sm">{gallery.error}</p>
        <button className="btn btn-ghost btn-sm" onClick={() => gallery.load(taskItems)}>
          重试
        </button>
      </div>
    );
  }

  if (filteredImages.length === 0) {
    return <EmptyState icon="detail" title="暂无图片" description="最近 3 天没有成功生成的图片" />;
  }

  return (
    <>
      {/* Stats */}
      <div className="px-4 py-2 text-xs text-base-content/40">
        共 {filteredImages.length} 张图片
      </div>

      {/* Masonry grid */}
      <div className="px-4 pb-20">
        <MasonryGrid>
          {filteredImages.map((img) => (
            <GalleryCard
              key={imageKey(img)}
              image={img}
              selected={selectedIds.has(imageKey(img))}
              onToggleSelect={() => toggleSelect(img)}
              onDownload={() => handleSingleDownload(img)}
              onPreview={() => openLightbox(img.imageUrl)}
            />
          ))}
        </MasonryGrid>
      </div>

      {/* Download bar */}
      <DownloadBar
        selectedCount={selectedIds.size}
        totalCount={filteredImages.length}
        downloading={downloading}
        downloadProgress={downloadProgress}
        onSelectAll={selectAll}
        onClearSelection={clearSelection}
        onDownloadZip={handleZipDownload}
      />

      {/* Lightbox */}
      <dialog ref={dialogRef} className="modal" onClick={closeLightbox}>
        <div className="modal-box max-w-5xl p-2 bg-base-300">
          {lightboxUrl && (
            <img src={lightboxUrl} alt="Full size preview" className="w-full rounded" />
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

- [ ] **Step 6: 在 index.css 末尾添加瀑布流样式**

```css
/* Masonry grid */
.masonry-grid {
  column-count: 4;
  column-gap: 12px;
}

@media (max-width: 1279px) {
  .masonry-grid {
    column-count: 3;
  }
}

@media (max-width: 767px) {
  .masonry-grid {
    column-count: 2;
    column-gap: 8px;
  }
}
```

- [ ] **Step 7: 验证编译**

```bash
cd cloudflare/task-dashboard && npx tsc --noEmit
```

- [ ] **Step 8: Commit**

```bash
git add src/hooks/useGallery.ts src/components/GalleryCard.tsx src/components/MasonryGrid.tsx src/components/DownloadBar.tsx src/components/GalleryView.tsx src/index.css
git commit -m "feat(task-dashboard): add gallery view with masonry grid, batch download, and lightbox"
```

---

### Task 9: DashboardPage 集成 GalleryView + TaskDetail 下载按钮

**Files:**
- Modify: `src/components/DashboardPage.tsx`
- Modify: `src/components/TaskDetail.tsx`

- [ ] **Step 1: DashboardPage 中替换相册占位为 GalleryView**

在 `DashboardPage.tsx` 中：

1. 顶部添加 import：
```tsx
import { GalleryView } from "./GalleryView";
```

2. 替换相册占位 div：
```tsx
// 将这段:
<div className="flex-1 overflow-y-auto bg-base-100 p-4">
  <div className="text-center text-base-content/40 py-16">
    相册视图（Task 8 实现）
  </div>
</div>

// 替换为:
<div className="flex-1 overflow-y-auto bg-base-100">
  <GalleryView
    apiKey={apiKey}
    taskItems={tasks.items}
    modelFilter={modelFilter}
  />
</div>
```

- [ ] **Step 2: TaskDetail 增加单张下载按钮**

在 `TaskDetail.tsx` 中：

1. 顶部添加 import：
```tsx
import { downloadSingleImage } from "../utils/download";
```

2. 在图片展示区域（`ImagePreview` 上方）添加下载按钮，替换 succeeded 区块：
```tsx
{detail.status === "succeeded" && (
  <>
    {textContent && (
      <div className="bg-base-200 rounded-lg p-3 text-sm whitespace-pre-wrap">
        {textContent}
      </div>
    )}
    <ImagePreview urls={imageURLs} />
    {imageURLs.length > 0 && (
      <div className="flex gap-2">
        {imageURLs.map((url, i) => (
          <button
            key={i}
            className="btn btn-ghost btn-xs gap-1"
            onClick={() =>
              downloadSingleImage(url, `task-${detail.id}-${i}.png`).catch(() =>
                alert("图片已过期或不可用")
              )
            }
          >
            <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
            </svg>
            下载图片{imageURLs.length > 1 ? ` ${i + 1}` : ""}
          </button>
        ))}
      </div>
    )}
  </>
)}
```

- [ ] **Step 3: 验证构建**

```bash
cd cloudflare/task-dashboard && npm run build
```

- [ ] **Step 4: Commit**

```bash
git add src/components/DashboardPage.tsx src/components/TaskDetail.tsx
git commit -m "feat(task-dashboard): integrate gallery view and add download buttons to task detail"
```

---

### Task 10: 集成验证

- [ ] **Step 1: 完整构建**

```bash
cd cloudflare/task-dashboard && npm run build
```

Expected: 构建成功

- [ ] **Step 2: 检查产物体积**

```bash
ls -lah dist/assets/
```

Expected: JS + CSS 合计 gzip 约 160-170KB

- [ ] **Step 3: 启动本地验证**

```bash
npm run dev
```

在浏览器中验证：
1. 登录 → 紧凑任务列表（一屏 15-20 个）
2. 任务卡片显示耗时（如 `50s`）
3. 提示文案："任务记录保持约 3 天 · 图片有效期约 3 小时"
4. 筛选：状态标签页 + 模型下拉
5. 加载更多按钮
6. TopBar：视图切换 + API Key 编辑
7. 切换到相册 → 瀑布流图片
8. 勾选图片 → 底部下载栏
9. 单张下载 + ZIP 批量下载
10. 任务详情 → 下载按钮

- [ ] **Step 4: Commit**

```bash
git add cloudflare/task-dashboard/
git commit -m "feat(task-dashboard): v2 complete - compact ui, filters, gallery, downloads"
```
