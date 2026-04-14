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
        {downloading ? (
          <span className="text-xs">
            {downloadProgress
              ? `正在打包 ${downloadProgress.completed}/${downloadProgress.total}...`
              : "正在准备..."}
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
