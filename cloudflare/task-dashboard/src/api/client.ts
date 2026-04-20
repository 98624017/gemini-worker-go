export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
    public retryAfter?: number
  ) {
    super(message);
    this.name = "ApiError";
  }
}

async function request<T>(
  path: string,
  apiKey: string,
  options?: RequestInit
): Promise<T> {
  const response = await fetch(path, {
    ...options,
    headers: {
      Authorization: `Bearer ${apiKey}`,
      Accept: "application/json",
      ...options?.headers,
    },
  });

  if (!response.ok) {
    const retryAfter = response.headers.get("Retry-After");
    let code = "unknown_error";
    let message = `HTTP ${response.status}`;

    try {
      const body = await response.json();
      if (body?.error) {
        code = body.error.code || code;
        message = body.error.message || message;
      }
    } catch {
      // Non-JSON error response
    }

    throw new ApiError(
      response.status,
      code,
      message,
      retryAfter ? parseInt(retryAfter, 10) : undefined
    );
  }

  return response.json() as Promise<T>;
}

// --- Types ---

export interface TaskListItem {
  id: string;
  model: string;
  status: string;
  created_at: number;
  finished_at?: number;
  content_url?: string;
}

export interface TaskListResponse {
  object: string;
  days: number;
  items: TaskListItem[];
}

export interface TaskDetailCandidate {
  content: {
    parts: Array<{
      text?: string;
      inlineData?: { mimeType: string; data: string };
    }>;
  };
  finishReason: string;
}

export interface TaskDetailResultImage {
  url: string;
}

export interface TaskDetailResult {
  created: number;
  data: TaskDetailResultImage[];
  usage?: Record<string, unknown>;
}

export interface TaskDetailResponse {
  id: string;
  object: string;
  model: string;
  status: string;
  created_at: number;
  finished_at?: number;
  response_id?: string;
  model_version?: string;
  usage_metadata?: Record<string, unknown>;
  candidates?: TaskDetailCandidate[];
  result?: TaskDetailResult;
  error?: { code: string; message: string };
  transport_uncertain?: boolean;
}

// --- API functions ---

export interface FetchTaskListOptions {
  limit?: number;
  beforeCreatedAt?: number;
  beforeId?: string;
}

export function fetchTaskList(
  apiKey: string,
  options?: FetchTaskListOptions
): Promise<TaskListResponse> {
  const limit = options?.limit ?? 100;
  let path = `/api/v1/tasks?limit=${limit}`;
  if (options?.beforeCreatedAt && options?.beforeId) {
    path += `&before_created_at=${options.beforeCreatedAt}&before_id=${encodeURIComponent(options.beforeId)}`;
  }
  return request<TaskListResponse>(path, apiKey);
}

export function fetchTaskDetail(
  apiKey: string,
  taskId: string
): Promise<TaskDetailResponse> {
  return request<TaskDetailResponse>(`/api/v1/tasks/${taskId}`, apiKey);
}

/** Extract image URLs from task detail response */
export function extractImageURLs(detail: TaskDetailResponse): string[] {
  if (detail.result?.data) {
    return detail.result.data
      .map((item) => item.url)
      .filter((url): url is string => typeof url === "string" && url.length > 0);
  }
  if (!detail.candidates) return [];
  const urls: string[] = [];
  for (const candidate of detail.candidates) {
    for (const part of candidate.content?.parts ?? []) {
      if (part.inlineData?.data) {
        urls.push(part.inlineData.data);
      }
    }
  }
  return urls;
}

/** Extract text content from task detail response */
export function extractTextContent(detail: TaskDetailResponse): string {
  if (!detail.candidates) return "";
  const texts: string[] = [];
  for (const candidate of detail.candidates) {
    for (const part of candidate.content?.parts ?? []) {
      if (part.text) {
        texts.push(part.text);
      }
    }
  }
  return texts.join("\n");
}

export function extractUsageMetadata(
  detail: TaskDetailResponse
): Record<string, unknown> | null {
  if (detail.result?.usage) return detail.result.usage;
  return detail.usage_metadata ?? null;
}

export interface BatchGetResponse {
  object: string;
  items: TaskDetailResponse[];
  next_poll_after_ms: number;
}

export function batchGetTasks(
  apiKey: string,
  ids: string[]
): Promise<BatchGetResponse> {
  return request<BatchGetResponse>("/api/v1/tasks/batch-get", apiKey, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ ids }),
  });
}
