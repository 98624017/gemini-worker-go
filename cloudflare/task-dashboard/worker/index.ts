import { serveStaticAsset, type StaticEnv } from "./static";
import { proxyApiRequest, proxyImageDownload, type ProxyEnv } from "./api-proxy";

export interface Env extends StaticEnv, ProxyEnv {
  TASK_CACHE: KVNamespace;
  OWNER_HASH_SECRET: string;
}

export default {
  async fetch(
    request: Request,
    env: Env,
    ctx: ExecutionContext
  ): Promise<Response> {
    const url = new URL(request.url);

    // Image download proxy
    if (url.pathname === "/api/download" && request.method === "GET") {
      return proxyImageDownload(request);
    }

    // API proxy: /api/v1/*
    if (url.pathname.startsWith("/api/v1/")) {
      return proxyApiRequest(request, env);
    }

    // Static assets / SPA fallback
    return serveStaticAsset(request, env, ctx);
  },
};
