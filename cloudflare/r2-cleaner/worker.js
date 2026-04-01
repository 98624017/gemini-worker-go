const DEFAULT_PREFIX = "images";
const DEFAULT_MAX_AGE_SECONDS = "10800";
const DELETE_BATCH_SIZE = 1000;

export function normalizePrefix(value) {
  return String(value ?? "").trim().replace(/^\/+|\/+$/g, "");
}

export function parsePositiveInt(value, fieldName, defaultValue) {
  const raw = String(value ?? defaultValue).trim();
  if (!/^\d+$/.test(raw)) {
    throw new Error(`${fieldName} must be a positive integer`);
  }
  const parsed = Number(raw);
  if (!Number.isSafeInteger(parsed) || parsed <= 0) {
    throw new Error(`${fieldName} must be a positive integer`);
  }
  return parsed;
}

export function getCleanupConfig(env) {
  return {
    prefix: normalizePrefix(env.R2_CLEANUP_PREFIX ?? DEFAULT_PREFIX),
    maxAgeSeconds: parsePositiveInt(
      env.R2_CLEANUP_MAX_AGE_SECONDS,
      "R2_CLEANUP_MAX_AGE_SECONDS",
      DEFAULT_MAX_AGE_SECONDS,
    ),
  };
}

export function collectExpiredKeys(objects, cutoff) {
  return objects
    .filter(
      (object) =>
        object &&
        typeof object.key === "string" &&
        object.uploaded instanceof Date &&
        object.uploaded <= cutoff,
    )
    .map((object) => object.key);
}

export function chunkKeys(keys, batchSize = DELETE_BATCH_SIZE) {
  if (!Array.isArray(keys) || keys.length === 0) {
    return [];
  }
  const chunks = [];
  for (let index = 0; index < keys.length; index += batchSize) {
    chunks.push(keys.slice(index, index + batchSize));
  }
  return chunks;
}

export async function runCleanup(env, options = {}) {
  const bucket = env?.R2_BUCKET;
  if (!bucket || typeof bucket.list !== "function" || typeof bucket.delete !== "function") {
    throw new Error("R2_BUCKET binding is required");
  }

  const now = options.now instanceof Date ? options.now : new Date();
  const log = options.log ?? console;
  const cron = options.cron;
  const { prefix, maxAgeSeconds } = getCleanupConfig(env);
  const cutoff = new Date(now.getTime() - maxAgeSeconds * 1000);

  let cursor;
  let listedCount = 0;
  let expiredCount = 0;
  let deletedCount = 0;
  let deleteErrorCount = 0;
  let pageCount = 0;

  do {
    const page = await bucket.list({ prefix, cursor });
    pageCount += 1;

    const objects = Array.isArray(page?.objects) ? page.objects : [];
    listedCount += objects.length;

    const expiredKeys = collectExpiredKeys(objects, cutoff);
    expiredCount += expiredKeys.length;

    const batches = chunkKeys(expiredKeys, DELETE_BATCH_SIZE);
    for (const batch of batches) {
      try {
        await bucket.delete(batch);
        deletedCount += batch.length;
      } catch (error) {
        deleteErrorCount += 1;
        log.error("r2 cleanup delete batch failed", {
          prefix,
          batchSize: batch.length,
          firstKey: batch[0],
          error: error instanceof Error ? error.message : String(error),
        });
      }
    }

    cursor = page?.truncated ? page.cursor : undefined;
  } while (cursor);

  const summary = {
    runAt: now.toISOString(),
    cron,
    prefix,
    maxAgeSeconds,
    cutoff: cutoff.toISOString(),
    listedCount,
    expiredCount,
    deletedCount,
    deleteErrorCount,
    pageCount,
  };

  log.log("r2 cleanup completed", summary);
  return summary;
}

const worker = {
  async scheduled(controller, env, ctx) {
    const promise = runCleanup(env, { log: console, cron: controller?.cron });
    if (ctx && typeof ctx.waitUntil === "function") {
      ctx.waitUntil(promise);
      return;
    }
    await promise;
  },
};

export default worker;
