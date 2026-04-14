// src/components/GalleryView.tsx
import { useEffect, useState, useCallback, useRef } from "react";
import { GalleryCard } from "./GalleryCard";
import { MasonryGrid } from "./MasonryGrid";
import { DownloadBar } from "./DownloadBar";
import { EmptyState } from "./EmptyState";
import { useGallery, type GalleryImage } from "../hooks/useGallery";
import { downloadSingleImage, downloadAsZip, type ZipProgress } from "../utils/download";
import type { TaskListItem } from "../api/client";

interface GalleryViewProps {
  apiKey: string;
  taskItems: TaskListItem[];
  modelFilter: string;
}

function useColumns(): number {
  const [cols, setCols] = useState(() =>
    typeof window !== "undefined" ? (window.innerWidth >= 1280 ? 4 : window.innerWidth >= 768 ? 3 : 2) : 4
  );
  useEffect(() => {
    function update() {
      setCols(window.innerWidth >= 1280 ? 4 : window.innerWidth >= 768 ? 3 : 2);
    }
    window.addEventListener("resize", update);
    return () => window.removeEventListener("resize", update);
  }, []);
  return cols;
}

export function GalleryView({ apiKey, taskItems, modelFilter }: GalleryViewProps) {
  const gallery = useGallery(apiKey);
  const columns = useColumns();
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [downloading, setDownloading] = useState(false);
  const [downloadProgress, setDownloadProgress] = useState<ZipProgress | null>(null);
  const [lightboxUrl, setLightboxUrl] = useState<string | null>(null);
  const dialogRef = useRef<HTMLDialogElement>(null);
  const loadedItemsRef = useRef<string>("");

  // Load gallery data when task items change
  useEffect(() => {
    if (taskItems.length === 0) return;
    // Track by ids to detect real changes (not just re-renders)
    const ids = taskItems.filter((t) => t.status === "succeeded").map((t) => t.id).join(",");
    if (ids === loadedItemsRef.current) return;
    loadedItemsRef.current = ids;
    gallery.load(taskItems);
  }, [taskItems]); // eslint-disable-line react-hooks/exhaustive-deps

  // Filter by model
  const filteredImages = modelFilter
    ? gallery.images.filter((img) => img.model === modelFilter)
    : gallery.images;

  const imageKey = (img: GalleryImage) => `${img.taskId}-${img.imageIndex}`;

  const toggleSelect = useCallback((img: GalleryImage) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      const key = imageKey(img);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  const selectAll = useCallback(() => {
    setSelectedIds(new Set(filteredImages.map(imageKey)));
  }, [filteredImages]);

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set());
  }, []);

  const handleSingleDownload = useCallback(async (img: GalleryImage) => {
    try {
      await downloadSingleImage(
        img.imageUrl,
        `task-${img.taskId}-${img.imageIndex}.png`
      );
    } catch {
      alert("图片已过期或不可用，请刷新页面");
    }
  }, []);

  const handleZipDownload = useCallback(async () => {
    const selected = filteredImages.filter((img) =>
      selectedIds.has(imageKey(img))
    );
    if (selected.length === 0) return;

    setDownloading(true);
    setDownloadProgress(null);

    const items = selected.map((img) => ({
      url: img.imageUrl,
      filename: `task-${img.taskId}-${img.imageIndex}.png`,
    }));

    try {
      const result = await downloadAsZip(items, setDownloadProgress);
      setSelectedIds(new Set());
      if (result.failed > 0) {
        alert(`已下载 ${result.downloaded} 张，${result.failed} 张因过期跳过`);
      }
    } catch {
      alert("打包下载失败，请重试");
    } finally {
      setDownloading(false);
      setDownloadProgress(null);
    }
  }, [filteredImages, selectedIds]);

  function openLightbox(url: string) {
    setLightboxUrl(url);
    dialogRef.current?.showModal();
  }

  function closeLightbox() {
    dialogRef.current?.close();
    setLightboxUrl(null);
  }

  if (gallery.loading) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-2">
        <span className="loading loading-spinner loading-md" />
        {gallery.progress && (
          <span className="text-xs text-base-content/40">
            正在加载图片... ({gallery.progress.current}/{gallery.progress.total} 批)
          </span>
        )}
      </div>
    );
  }

  if (gallery.error) {
    return (
      <div className="flex flex-col items-center justify-center py-20 gap-2">
        <p className="text-error text-sm">{gallery.error}</p>
        <button className="btn btn-ghost btn-sm" onClick={() => gallery.load(taskItems)}>
          重试
        </button>
      </div>
    );
  }

  if (filteredImages.length === 0) {
    return <EmptyState icon="detail" title="暂无图片" description="最近 3 天没有成功生成的图片" />;
  }

  return (
    <>
      {/* Stats */}
      <div className="px-4 py-2 text-xs text-base-content/40">
        共 {filteredImages.length} 张图片
      </div>

      {/* Masonry grid */}
      <div className="px-4 pb-20">
        <MasonryGrid columns={columns}>
          {filteredImages.map((img) => (
            <GalleryCard
              key={imageKey(img)}
              image={img}
              selected={selectedIds.has(imageKey(img))}
              onToggleSelect={() => toggleSelect(img)}
              onDownload={() => handleSingleDownload(img)}
              onPreview={() => openLightbox(img.imageUrl)}
            />
          ))}
        </MasonryGrid>
      </div>

      {/* Download bar */}
      <DownloadBar
        selectedCount={selectedIds.size}
        totalCount={filteredImages.length}
        downloading={downloading}
        downloadProgress={downloadProgress}
        onSelectAll={selectAll}
        onClearSelection={clearSelection}
        onDownloadZip={handleZipDownload}
      />

      {/* Lightbox */}
      <dialog ref={dialogRef} className="modal" onClick={closeLightbox}>
        <div className="modal-box max-w-5xl p-2 bg-base-300" onClick={(e) => e.stopPropagation()}>
          {lightboxUrl && (
            <img src={lightboxUrl} alt="Full size preview" className="w-full rounded" />
          )}
        </div>
        <form method="dialog" className="modal-backdrop">
          <button>close</button>
        </form>
      </dialog>
    </>
  );
}
