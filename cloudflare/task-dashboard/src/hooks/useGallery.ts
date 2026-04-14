// src/hooks/useGallery.ts
import { useState, useCallback } from "react";
import {
  batchGetTasks,
  extractImageURLs,
  type TaskListItem,
  type TaskDetailResponse,
} from "../api/client";

export interface GalleryImage {
  taskId: string;
  model: string;
  createdAt: number;
  imageUrl: string;
  imageIndex: number;
}

interface UseGalleryState {
  images: GalleryImage[];
  loading: boolean;
  error: string | null;
  progress: { current: number; total: number } | null;
}

export function useGallery(apiKey: string | null) {
  const [state, setState] = useState<UseGalleryState>({
    images: [],
    loading: false,
    error: null,
    progress: null,
  });

  const load = useCallback(
    async (taskItems: TaskListItem[]) => {
      if (!apiKey) return;

      const succeededIds = taskItems
        .filter((t) => t.status === "succeeded")
        .map((t) => t.id);

      if (succeededIds.length === 0) {
        setState({ images: [], loading: false, error: null, progress: null });
        return;
      }

      setState({ images: [], loading: true, error: null, progress: null });

      try {
        // Split into batches of 100
        const batches: string[][] = [];
        for (let i = 0; i < succeededIds.length; i += 100) {
          batches.push(succeededIds.slice(i, i + 100));
        }

        const allImages: GalleryImage[] = [];

        for (let i = 0; i < batches.length; i++) {
          setState((prev) => ({
            ...prev,
            progress: { current: i + 1, total: batches.length },
          }));

          const response = await batchGetTasks(apiKey, batches[i]);

          for (const item of response.items) {
            const detail = item as TaskDetailResponse;
            if (detail.status !== "succeeded") continue;

            const urls = extractImageURLs(detail);
            for (let idx = 0; idx < urls.length; idx++) {
              allImages.push({
                taskId: detail.id,
                model: detail.model,
                createdAt: detail.created_at,
                imageUrl: urls[idx],
                imageIndex: idx,
              });
            }
          }
        }

        // Sort newest first
        allImages.sort((a, b) => b.createdAt - a.createdAt);

        setState({
          images: allImages,
          loading: false,
          error: null,
          progress: null,
        });
      } catch (err) {
        setState({
          images: [],
          loading: false,
          error: err instanceof Error ? err.message : "加载失败",
          progress: null,
        });
      }
    },
    [apiKey]
  );

  return { ...state, load };
}
