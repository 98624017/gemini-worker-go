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
