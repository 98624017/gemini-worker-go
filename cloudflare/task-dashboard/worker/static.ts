import { getAssetFromKV } from "@cloudflare/kv-asset-handler";
// @ts-expect-error — __STATIC_CONTENT_MANIFEST is injected by wrangler at build time
import manifestJSON from "__STATIC_CONTENT_MANIFEST";

const assetManifest = JSON.parse(manifestJSON);

export interface StaticEnv {
  __STATIC_CONTENT: KVNamespace;
}

export async function serveStaticAsset(
  request: Request,
  env: StaticEnv,
  ctx: ExecutionContext
): Promise<Response> {
  const url = new URL(request.url);

  try {
    const response = await getAssetFromKV(
      { request, waitUntil: ctx.waitUntil.bind(ctx) },
      {
        ASSET_NAMESPACE: env.__STATIC_CONTENT,
        ASSET_MANIFEST: assetManifest,
      }
    );

    if (url.pathname.startsWith("/assets/")) {
      const headers = new Headers(response.headers);
      headers.set("Cache-Control", "public, max-age=31536000, immutable");
      return new Response(response.body, { ...response, headers });
    }

    const headers = new Headers(response.headers);
    headers.set("Cache-Control", "no-cache");
    return new Response(response.body, { ...response, headers });
  } catch {
    const fallbackRequest = new Request(
      new URL("/index.html", request.url).toString(),
      request
    );
    try {
      const response = await getAssetFromKV(
        { request: fallbackRequest, waitUntil: ctx.waitUntil.bind(ctx) },
        {
          ASSET_NAMESPACE: env.__STATIC_CONTENT,
          ASSET_MANIFEST: assetManifest,
        }
      );
      const headers = new Headers(response.headers);
      headers.set("Cache-Control", "no-cache");
      return new Response(response.body, { ...response, headers });
    } catch {
      return new Response("Not Found", { status: 404 });
    }
  }
}
