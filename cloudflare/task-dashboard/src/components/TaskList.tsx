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
        <p className="text-[11px] text-base-content/30 mt-0.5">
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
      <div className="flex-1 overflow-y-auto px-1.5 py-1 space-y-1">
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
