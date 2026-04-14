import { useState, useCallback } from "react";
import {
  fetchTaskList,
  ApiError,
  type TaskListItem,
} from "../api/client";

const PAGE_SIZE = 100;

function delay(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

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
      if (err instanceof ApiError && err.status === 429 && err.retryAfter) {
        // Rate limited — wait and retry once
        await delay(err.retryAfter * 1000);
        try {
          const data = await fetchTaskList(apiKey, { limit: PAGE_SIZE });
          setState({
            items: data.items,
            loading: false,
            loadingMore: false,
            hasMore: data.items.length >= PAGE_SIZE,
            error: null,
          });
          return;
        } catch {
          // Fall through to error handling
        }
      }

      const message =
        err instanceof ApiError
          ? err.status === 429
            ? "请求过于频繁，请稍后重试"
            : err.message
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
    const fetchPage = () =>
      fetchTaskList(apiKey, {
        limit: PAGE_SIZE,
        beforeCreatedAt: lastItem.created_at,
        beforeId: lastItem.id,
      });

    try {
      const data = await fetchPage();
      setState((prev) => ({
        ...prev,
        items: [...prev.items, ...data.items],
        loadingMore: false,
        hasMore: data.items.length >= PAGE_SIZE,
      }));
    } catch (err) {
      if (err instanceof ApiError && err.status === 429 && err.retryAfter) {
        // Rate limited — wait and retry once
        await delay(err.retryAfter * 1000);
        try {
          const data = await fetchPage();
          setState((prev) => ({
            ...prev,
            items: [...prev.items, ...data.items],
            loadingMore: false,
            hasMore: data.items.length >= PAGE_SIZE,
          }));
          return;
        } catch {
          // Fall through to error handling
        }
      }

      const message =
        err instanceof ApiError
          ? err.status === 429
            ? "请求过于频繁，请稍后重试"
            : err.message
          : "加载更多失败";
      setState((prev) => ({ ...prev, loadingMore: false, error: message }));
    }
  }, [apiKey, state.items]);

  return { ...state, load, loadMore };
}
