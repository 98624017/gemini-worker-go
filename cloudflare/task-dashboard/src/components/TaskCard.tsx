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
      className={`flex items-center gap-2 px-2.5 py-2 rounded-md cursor-pointer border transition-all duration-150 animate-card-enter ${
        isSelected
          ? "border-primary/60 bg-primary/10"
          : "border-transparent hover:bg-base-200/60"
      }`}
      style={{ animationDelay: `${Math.min(index, 15) * 30}ms` }}
      onClick={onClick}
    >
      <span className="text-xs font-medium text-base-content/70 bg-base-200 px-1.5 py-0.5 rounded shrink-0">
        {shortModel}
      </span>

      <span className="font-mono text-xs text-base-content/40 truncate flex-1 min-w-0">
        {task.id}
      </span>

      <span
        className="text-[11px] text-base-content/40 shrink-0"
        title={formatTime(task.created_at)}
      >
        {timeAgo(task.created_at)}
        {duration && <span className="text-base-content/30"> · {duration}</span>}
      </span>

      <StatusBadge status={task.status} />
    </div>
  );
}
