// src/utils/download.ts
import JSZip from "jszip";
import { saveAs } from "file-saver";

/** Download a single image via the Worker proxy */
export async function downloadSingleImage(
  imageUrl: string,
  filename: string
): Promise<void> {
  const proxyUrl = `/api/download?url=${encodeURIComponent(imageUrl)}`;
  const response = await fetch(proxyUrl);

  if (!response.ok) {
    throw new Error("图片已过期或不可用");
  }

  const blob = await response.blob();
  saveAs(blob, filename);
}

export interface DownloadItem {
  url: string;
  filename: string;
}

export interface ZipProgress {
  completed: number;
  total: number;
  failed: number;
}

/** Download multiple images and pack into a ZIP file */
export async function downloadAsZip(
  items: DownloadItem[],
  onProgress?: (progress: ZipProgress) => void
): Promise<{ downloaded: number; failed: number }> {
  const zip = new JSZip();
  let completed = 0;
  let failed = 0;

  for (const item of items) {
    try {
      const proxyUrl = `/api/download?url=${encodeURIComponent(item.url)}`;
      const response = await fetch(proxyUrl);

      if (!response.ok) {
        failed++;
      } else {
        const blob = await response.blob();
        zip.file(item.filename, blob);
      }
    } catch {
      failed++;
    }

    completed++;
    onProgress?.({ completed, total: items.length, failed });
  }

  const downloaded = completed - failed;

  if (downloaded > 0) {
    const zipBlob = await zip.generateAsync({ type: "blob" });
    const timestamp = new Date().toISOString().slice(0, 19).replace(/[T:]/g, "-");
    saveAs(zipBlob, `images-${timestamp}.zip`);
  }

  return { downloaded, failed };
}
