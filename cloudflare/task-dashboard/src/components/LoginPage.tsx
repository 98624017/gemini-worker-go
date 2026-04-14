import { useState, type FormEvent } from "react";
import { fetchTaskList, ApiError, type TaskListItem } from "../api/client";

interface LoginPageProps {
  onLogin: (apiKey: string, initialItems: TaskListItem[]) => void;
}

export function LoginPage({ onLogin }: LoginPageProps) {
  const [key, setKey] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function handleSubmit(e: FormEvent) {
    e.preventDefault();
    const trimmed = key.trim();
    if (!trimmed) return;

    setLoading(true);
    setError(null);

    try {
      const data = await fetchTaskList(trimmed);
      onLogin(trimmed, data.items);
    } catch (err) {
      if (err instanceof ApiError) {
        if (err.status === 401) {
          setError("API Key 无效，请检查后重试");
        } else {
          setError(err.message);
        }
      } else if (err instanceof TypeError) {
        setError("网络连接失败，请检查网络");
      } else {
        setError("验证失败，请重试");
      }
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-base-200 px-4">
      <div className="card w-full max-w-md bg-base-100 shadow-xl">
        <div className="card-body">
          <h2 className="card-title text-2xl font-bold justify-center mb-2">
            Task Dashboard
          </h2>
          <p className="text-base-content/60 text-center text-sm mb-6">
            输入 API Key 查看最近 3 天的生图任务
          </p>

          <form onSubmit={handleSubmit}>
            <div className="form-control">
              <input
                type="password"
                placeholder="请输入 API Key"
                className="input input-bordered w-full"
                value={key}
                onChange={(e) => setKey(e.target.value)}
                disabled={loading}
                autoFocus
              />
            </div>

            {error && (
              <div className="alert alert-error mt-4 py-2 text-sm">
                <svg
                  xmlns="http://www.w3.org/2000/svg"
                  className="stroke-current shrink-0 h-5 w-5"
                  fill="none"
                  viewBox="0 0 24 24"
                >
                  <path
                    strokeLinecap="round"
                    strokeLinejoin="round"
                    strokeWidth="2"
                    d="M10 14l2-2m0 0l2-2m-2 2l-2-2m2 2l2 2m7-2a9 9 0 11-18 0 9 9 0 0118 0z"
                  />
                </svg>
                <span>{error}</span>
              </div>
            )}

            <div className="form-control mt-6">
              <button
                type="submit"
                className={`btn btn-primary w-full ${loading ? "loading" : ""}`}
                disabled={loading || !key.trim()}
              >
                {loading ? "验证中..." : "查询任务"}
              </button>
            </div>
          </form>
        </div>
      </div>
    </div>
  );
}
