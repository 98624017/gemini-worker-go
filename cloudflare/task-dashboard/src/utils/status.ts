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

export function isInProgressStatus(status: string): boolean {
  return status === "accepted" || status === "queued" || status === "running";
}
