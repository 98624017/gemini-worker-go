import { useState, useRef } from "react";

interface ImagePreviewProps {
  urls: string[];
}

export function ImagePreview({ urls }: ImagePreviewProps) {
  const [lightboxUrl, setLightboxUrl] = useState<string | null>(null);
  const [loadedMap, setLoadedMap] = useState<Record<number, boolean>>({});
  const [errorMap, setErrorMap] = useState<Record<number, boolean>>({});
  const dialogRef = useRef<HTMLDialogElement>(null);

  function openLightbox(url: string) {
    setLightboxUrl(url);
    dialogRef.current?.showModal();
  }

  function closeLightbox() {
    dialogRef.current?.close();
    setLightboxUrl(null);
  }

  if (urls.length === 0) return null;

  return (
    <>
      <div className="grid grid-cols-1 gap-3">
        {urls.map((url, i) => (
          <div key={i} className="relative">
            {!loadedMap[i] && !errorMap[i] && (
              <div className="aspect-square rounded-lg animate-shimmer bg-base-300" />
            )}

            {errorMap[i] && (
              <div className="aspect-square rounded-lg bg-base-300 flex flex-col items-center justify-center gap-2">
                <span className="text-base-content/40 text-sm">加载失败</span>
                <button
                  className="btn btn-ghost btn-xs"
                  onClick={() => {
                    setErrorMap((prev) => ({ ...prev, [i]: false }));
                    setLoadedMap((prev) => ({ ...prev, [i]: false }));
                  }}
                >
                  重试
                </button>
              </div>
            )}

            {!errorMap[i] && (
              <img
                src={url}
                alt={`Generated image ${i + 1}`}
                className={`w-full rounded-lg cursor-pointer hover:ring-2 hover:ring-primary transition-all duration-400 ${
                  loadedMap[i] ? "opacity-100" : "opacity-0 absolute inset-0"
                }`}
                onLoad={() =>
                  setLoadedMap((prev) => ({ ...prev, [i]: true }))
                }
                onError={() =>
                  setErrorMap((prev) => ({ ...prev, [i]: true }))
                }
                onClick={() => openLightbox(url)}
              />
            )}
          </div>
        ))}
      </div>

      <dialog ref={dialogRef} className="modal" onClick={closeLightbox}>
        <div className="modal-box max-w-5xl p-2 bg-base-300" onClick={(e) => e.stopPropagation()}>
          {lightboxUrl && (
            <img
              src={lightboxUrl}
              alt="Full size preview"
              className="w-full rounded"
            />
          )}
        </div>
        <form method="dialog" className="modal-backdrop">
          <button>close</button>
        </form>
      </dialog>
    </>
  );
}
