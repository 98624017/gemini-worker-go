import { getStatusConfig } from "../utils/status";

interface StatusBadgeProps {
  status: string;
}

export function StatusBadge({ status }: StatusBadgeProps) {
  const config = getStatusConfig(status);

  return (
    <span
      className={`badge badge-xs text-[11px] px-2 ${config.badgeClass} ${
        config.animate ? "animate-status-pulse" : ""
      }`}
    >
      {config.label}
    </span>
  );
}
