import { useState, useRef, useEffect, type FormEvent } from "react";
import { fetchTaskList, ApiError } from "../api/client";

interface ApiKeyInputProps {
  currentKey: string;
  onChangeKey: (newKey: string) => void;
}

export function ApiKeyInput({ currentKey, onChangeKey }: ApiKeyInputProps) {
  const [editing, setEditing] = useState(false);
  const [value, setValue] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
    }
  }, [editing]);

  const masked = currentKey
    ? `sk-****${currentKey.slice(-4)}`
    : "未设置";

  function startEdit() {
    setValue("");
    setError(null);
    setEditing(true);
  }

  function cancelEdit() {
    setEditing(false);
    setError(null);
  }

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = value.trim();
    if (!trimmed) return;

    setLoading(true);
    setError(null);

    try {
      await fetchTaskList(trimmed, { limit: 1 });
      onChangeKey(trimmed);
      setEditing(false);
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setError("Key 无效");
      } else {
        setError("验证失败");
      }
    } finally {
      setLoading(false);
    }
  }

  if (!editing) {
    return (
      <button
        className="btn btn-ghost btn-xs text-[11px] font-mono gap-1"
        onClick={startEdit}
        title="更换 API Key"
      >
        {masked}
        <svg xmlns="http://www.w3.org/2000/svg" className="h-3 w-3" fill="none" viewBox="0 0 24 24" stroke="currentColor">
          <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M15.232 5.232l3.536 3.536m-2.036-5.036a2.5 2.5 0 113.536 3.536L6.5 21.036H3v-3.572L16.732 3.732z" />
        </svg>
      </button>
    );
  }

  return (
    <form onSubmit={handleSubmit} className="flex items-center gap-1">
      <input
        ref={inputRef}
        type="password"
        className={`input input-xs input-bordered w-40 text-[11px] ${error ? "input-error" : ""}`}
        placeholder="输入新 API Key"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        onKeyDown={(e) => e.key === "Escape" && cancelEdit()}
        disabled={loading}
      />
      <button
        type="submit"
        className="btn btn-xs btn-primary"
        disabled={loading || !value.trim()}
      >
        {loading ? <span className="loading loading-spinner loading-xs" /> : "确认"}
      </button>
      <button
        type="button"
        className="btn btn-xs btn-ghost"
        onClick={cancelEdit}
        disabled={loading}
      >
        取消
      </button>
      {error && <span className="text-[10px] text-error">{error}</span>}
    </form>
  );
}
