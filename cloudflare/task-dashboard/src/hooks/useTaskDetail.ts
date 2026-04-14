import { useState, useCallback } from "react";
import {
  fetchTaskDetail,
  ApiError,
  type TaskDetailResponse,
} from "../api/client";

interface UseTaskDetailState {
  detail: TaskDetailResponse | null;
  loading: boolean;
  error: string | null;
}

export function useTaskDetail(apiKey: string | null) {
  const [state, setState] = useState<UseTaskDetailState>({
    detail: null,
    loading: false,
    error: null,
  });

  const load = useCallback(
    async (taskId: string) => {
      if (!apiKey) return;

      setState({ detail: null, loading: true, error: null });

      try {
        const data = await fetchTaskDetail(apiKey, taskId);
        setState({ detail: data, loading: false, error: null });
      } catch (err) {
        const message =
          err instanceof ApiError
            ? err.message
            : err instanceof TypeError
              ? "网络连接失败"
              : "加载详情失败";
        setState({ detail: null, loading: false, error: message });
      }
    },
    [apiKey]
  );

  const clear = useCallback(() => {
    setState({ detail: null, loading: false, error: null });
  }, []);

  return { ...state, load, clear };
}
