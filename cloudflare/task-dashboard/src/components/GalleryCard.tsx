// src/components/GalleryCard.tsx
import { useState } from "react";
import { timeAgo } from "../utils/time";
import type { GalleryImage } from "../hooks/useGallery";

interface GalleryCardProps {
  image: GalleryImage;
  selected: boolean;
  onToggleSelect: () => void;
  onDownload: () => void;
  onPreview: () => void;
}

export function GalleryCard({
  image,
  selected,
  onToggleSelect,
  onDownload,
  onPreview,
}: GalleryCardProps) {
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState(false);

  const shortModel = image.model
    .replace("gemini-3-pro-image-preview-", "")
    .replace("gemini-3-pro-image-preview", "imagen");

  return (
    <div
      className={`group relative rounded-lg overflow-hidden border-2 transition-all duration-150 mb-3 break-inside-avoid ${
        selected ? "border-primary ring-2 ring-primary/30" : "border-transparent"
      }`}
    >
      {/* Checkbox */}
      <label
        className={`absolute top-2 left-2 z-10 cursor-pointer transition-opacity ${
          selected ? "opacity-100" : "opacity-0 group-hover:opacity-100"
        }`}
        onClick={(e) => e.stopPropagation()}
      >
        <input
          type="checkbox"
          className="checkbox checkbox-primary checkbox-xs"
          checked={selected}
          onChange={onToggleSelect}
        />
      </label>

      {/* Download button */}
      <button
        className="absolute bottom-10 right-2 z-10 btn btn-circle btn-xs bg-base-100/80 border-none opacity-0 group-hover:opacity-100 transition-opacity"
        onClick={(e) => {
          e.stopPropagation();
          onDownload();
        }}
        title="下载"
      >
        <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-4l-4 4m0 0l-4-4m4 4V4" />
        </svg>
      </button>

      {/* Image */}
      {!loaded && !error && (
        <div className="aspect-square animate-shimmer bg-base-300" />
      )}
      {error && (
        <div className="aspect-[4/3] bg-base-300/50 flex items-center justify-center rounded-sm">
          <span className="text-xs text-base-content/30">已过期</span>
        </div>
      )}
      {!error && (
        <img
          src={image.imageUrl}
          alt=""
          className={`w-full cursor-pointer group-hover:brightness-110 transition-all ${
            loaded ? "opacity-100" : "opacity-0 absolute"
          }`}
          onLoad={() => setLoaded(true)}
          onError={() => setError(true)}
          onClick={onPreview}
        />
      )}

      {/* Info bar */}
      <div className="px-2 py-1.5 bg-base-200 text-xs text-base-content/70 flex justify-between">
        <span>{shortModel}</span>
        <span>{timeAgo(image.createdAt)}</span>
      </div>
    </div>
  );
}
