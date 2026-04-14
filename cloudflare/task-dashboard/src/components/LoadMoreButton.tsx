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
