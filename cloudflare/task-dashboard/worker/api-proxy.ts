import { deriveOwnerHash } from "./owner-hash";

export interface ProxyEnv {
  BACKEND_URL: string;
  TASK_CACHE: KVNamespace;
  OWNER_HASH_SECRET: string;
}

const CACHE_TTL_SECONDS = 86400; // 24 hours
const TERMINAL_STATUSES = new Set(["succeeded", "failed"]);

/** Match /api/v1/tasks/{taskID} but not /api/v1/tasks or /api/v1/tasks/{id}/content */
function extractTaskID(pathname: string): string | null {
  const match = pathname.match(/^\/api\/v1\/tasks\/([^/]+)$/);
  if (!match || match[1] === "batch-get") return null;
  return match[1];
}

export async function proxyApiRequest(
  request: Request,
  env: ProxyEnv
): Promise<Response> {
  const url = new URL(request.url);
  const backendURL = (env.BACKEND_URL || "https://async.xinbao-ai.com").replace(
    /\/$/,
    ""
  );

  const authorization = request.headers.get("Authorization") || "";

  // KV cache: only for GET /api/v1/tasks/:id
  const taskID = request.method === "GET" ? extractTaskID(url.pathname) : null;
  if (taskID && env.TASK_CACHE && env.OWNER_HASH_SECRET) {
    try {
      const cached = await tryKVCache(env, taskID, authorization);
      if (cached) return cached;
    } catch {
      // Cache miss or error — fall through to origin
    }
  }

  // Forward to origin
  const backendPath = url.pathname.replace(/^\/api/, "");
  const targetURL = backendURL + backendPath + url.search;

  const headers = new Headers();
  if (authorization) {
    headers.set("Authorization", authorization);
  }
  headers.set("Content-Type", "application/json");
  headers.set("Accept", "application/json");

  const backendResponse = await fetch(targetURL, {
    method: request.method,
    headers,
    body:
      request.method !== "GET" && request.method !== "HEAD"
        ? request.body
        : undefined,
  });

  // If this is a successful task detail response, check if we should cache it
  if (
    taskID &&
    backendResponse.status === 200 &&
    env.TASK_CACHE &&
    env.OWNER_HASH_SECRET
  ) {
    const body = await backendResponse.text();
    try {
      const data = JSON.parse(body);
      if (data.status && TERMINAL_STATUSES.has(data.status)) {
        const ownerHash = await deriveOwnerHash(
          env.OWNER_HASH_SECRET,
          authorization
        );
        const cacheEntry = JSON.stringify({
          owner_hash: ownerHash,
          response_body: data,
          cached_at: Math.floor(Date.now() / 1000),
        });
        env.TASK_CACHE.put(`task:${taskID}`, cacheEntry, {
          expirationTtl: CACHE_TTL_SECONDS,
        }).catch(() => {});
      }
    } catch {
      // JSON parse error — skip caching
    }

    const responseHeaders = new Headers(backendResponse.headers);
    responseHeaders.delete("transfer-encoding");
    return new Response(body, {
      status: backendResponse.status,
      statusText: backendResponse.statusText,
      headers: responseHeaders,
    });
  }

  const responseHeaders = new Headers(backendResponse.headers);
  responseHeaders.delete("transfer-encoding");
  return new Response(backendResponse.body, {
    status: backendResponse.status,
    statusText: backendResponse.statusText,
    headers: responseHeaders,
  });
}

/** Only allow HTTPS URLs for download proxy */
function isUrlAllowed(url: string): boolean {
  try {
    return new URL(url).protocol === "https:";
  } catch {
    return false;
  }
}

export async function proxyImageDownload(
  request: Request
): Promise<Response> {
  const url = new URL(request.url);
  const imageUrl = url.searchParams.get("url");

  if (!imageUrl) {
    return new Response(JSON.stringify({ error: "missing url parameter" }), {
      status: 400,
      headers: { "Content-Type": "application/json" },
    });
  }

  if (!isUrlAllowed(imageUrl)) {
    return new Response(JSON.stringify({ error: "only https urls allowed" }), {
      status: 403,
      headers: { "Content-Type": "application/json" },
    });
  }

  try {
    const response = await fetch(imageUrl);
    if (!response.ok) {
      return new Response(JSON.stringify({ error: `upstream ${response.status}` }), {
        status: response.status,
        headers: { "Content-Type": "application/json" },
      });
    }

    const headers = new Headers();
    const contentType = response.headers.get("Content-Type");
    if (contentType) headers.set("Content-Type", contentType);
    headers.set("Cache-Control", "no-store");

    return new Response(response.body, { status: 200, headers });
  } catch {
    return new Response(JSON.stringify({ error: "fetch failed" }), {
      status: 502,
      headers: { "Content-Type": "application/json" },
    });
  }
}

async function tryKVCache(
  env: ProxyEnv,
  taskID: string,
  authorization: string
): Promise<Response | null> {
  const raw = await env.TASK_CACHE.get(`task:${taskID}`);
  if (!raw) return null;

  const entry = JSON.parse(raw) as {
    owner_hash: string;
    response_body: unknown;
    cached_at: number;
  };

  const ownerHash = await deriveOwnerHash(env.OWNER_HASH_SECRET, authorization);
  if (entry.owner_hash !== ownerHash) return null;

  return new Response(JSON.stringify(entry.response_body), {
    status: 200,
    headers: {
      "Content-Type": "application/json",
      "X-Cache": "HIT",
    },
  });
}
