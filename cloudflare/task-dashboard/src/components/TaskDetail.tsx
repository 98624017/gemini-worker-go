import { useEffect, useRef, useState } from "react";
import { StatusBadge } from "./StatusBadge";
import { ImagePreview } from "./ImagePreview";
import { MetadataPanel } from "./MetadataPanel";
import { ErrorPanel } from "./ErrorPanel";
import { EmptyState } from "./EmptyState";
import { formatTime } from "../utils/time";
import { downloadSingleImage } from "../utils/download";
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
  const [animKey, setAnimKey] = useState(0);

  useEffect(() => {
    if (detail) {
      setAnimKey((k) => k + 1);
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
    <div key={animKey} ref={containerRef} className="h-full overflow-y-auto p-4 space-y-4 animate-detail-enter">
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

      <div className="flex items-center justify-between flex-wrap gap-2">
        <div>
          <h3 className="font-mono text-sm text-base-content/50">{detail.id}</h3>
          <p className="text-lg font-semibold">{detail.model}</p>
        </div>
        <StatusBadge status={detail.status} />
      </div>

      <div className="flex gap-4 text-xs text-base-content/50">
        <span>创建: {formatTime(detail.created_at)}</span>
        {detail.finished_at && <span>完成: {formatTime(detail.finished_at)}</span>}
        {detail.model_version && <span>版本: {detail.model_version}</span>}
      </div>

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

      {(detail.status === "failed" || detail.status === "uncertain") &&
        detail.error && (
          <ErrorPanel
            code={detail.error.code}
            message={detail.error.message}
            uncertain={detail.transport_uncertain}
          />
        )}

      {detail.usage_metadata && (
        <MetadataPanel metadata={detail.usage_metadata} />
      )}
    </div>
  );
}
