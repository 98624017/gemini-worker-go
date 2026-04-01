import test from "node:test";
import assert from "node:assert/strict";

import worker, {
  chunkKeys,
  collectExpiredKeys,
  getCleanupConfig,
  normalizePrefix,
  parsePositiveInt,
  runCleanup,
} from "./worker.js";

test("getCleanupConfig uses default values", () => {
  const config = getCleanupConfig({});
  assert.equal(config.prefix, "images");
  assert.equal(config.maxAgeSeconds, 10800);
});

test("normalizePrefix trims spaces and slashes", () => {
  assert.equal(normalizePrefix(" /images/ "), "images");
  assert.equal(normalizePrefix("images///"), "images");
});

test("getCleanupConfig allows empty prefix", () => {
  const config = getCleanupConfig({ R2_CLEANUP_PREFIX: " / " });
  assert.equal(config.prefix, "");
});

test("parsePositiveInt rejects invalid numbers", () => {
  assert.throws(
    () => parsePositiveInt("0", "R2_CLEANUP_MAX_AGE_SECONDS", "10800"),
    /positive integer/,
  );
  assert.throws(
    () => parsePositiveInt("abc", "R2_CLEANUP_MAX_AGE_SECONDS", "10800"),
    /positive integer/,
  );
});

test("collectExpiredKeys returns objects older than cutoff", () => {
  const cutoff = new Date("2026-04-01T09:00:00Z");
  const objects = [
    { key: "images/a.png", uploaded: new Date("2026-04-01T08:00:00Z") },
    { key: "images/b.png", uploaded: new Date("2026-04-01T10:00:00Z") },
    { key: "images/c.png", uploaded: new Date("2026-04-01T09:00:00Z") },
  ];
  assert.deepEqual(collectExpiredKeys(objects, cutoff), [
    "images/a.png",
    "images/c.png",
  ]);
});

test("chunkKeys splits by 1000", () => {
  const keys = Array.from({ length: 1001 }, (_, i) => `images/${i}.png`);
  const batches = chunkKeys(keys, 1000);
  assert.equal(batches.length, 2);
  assert.equal(batches[0].length, 1000);
  assert.equal(batches[1].length, 1);
});

test("runCleanup pages through list and deletes only expired keys", async () => {
  const deletedBatches = [];
  const listCalls = [];
  const fakeBucket = {
    async list(options) {
      listCalls.push(options);
      if (!options.cursor) {
        return {
          objects: [
            { key: "images/a.png", uploaded: new Date("2026-04-01T00:00:00Z") },
            { key: "images/b.png", uploaded: new Date("2026-04-01T11:30:00Z") },
          ],
          truncated: true,
          cursor: "next",
        };
      }
      return {
        objects: [
          { key: "images/c.png", uploaded: new Date("2026-04-01T01:00:00Z") },
        ],
        truncated: false,
      };
    },
    async delete(keys) {
      deletedBatches.push(keys);
    },
  };

  const summary = await runCleanup(
    {
      R2_BUCKET: fakeBucket,
      R2_CLEANUP_PREFIX: " /images/ ",
      R2_CLEANUP_MAX_AGE_SECONDS: "10800",
    },
    {
      now: new Date("2026-04-01T12:00:00Z"),
      log: { log() {}, error() {} },
    },
  );

  assert.equal(listCalls.length, 2);
  assert.deepEqual(listCalls[0], { prefix: "images", cursor: undefined });
  assert.deepEqual(listCalls[1], { prefix: "images", cursor: "next" });
  assert.deepEqual(deletedBatches, [["images/a.png"], ["images/c.png"]]);
  assert.equal(summary.listedCount, 3);
  assert.equal(summary.expiredCount, 2);
  assert.equal(summary.deletedCount, 2);
  assert.equal(summary.deleteErrorCount, 0);
  assert.equal(summary.pageCount, 2);
});

test("runCleanup continues when a delete batch fails", async () => {
  const deleteCalls = [];
  const logErrors = [];
  const oldObjects = Array.from({ length: 1001 }, (_, i) => ({
    key: `images/${i}.png`,
    uploaded: new Date("2026-04-01T00:00:00Z"),
  }));

  const fakeBucket = {
    async list() {
      return { objects: oldObjects, truncated: false };
    },
    async delete(keys) {
      deleteCalls.push(keys.length);
      if (keys.length === 1000) {
        throw new Error("delete failed for first batch");
      }
    },
  };

  const summary = await runCleanup(
    {
      R2_BUCKET: fakeBucket,
      R2_CLEANUP_PREFIX: "images",
      R2_CLEANUP_MAX_AGE_SECONDS: "10800",
    },
    {
      now: new Date("2026-04-01T12:00:00Z"),
      log: {
        log() {},
        error(message, payload) {
          logErrors.push({ message, payload });
        },
      },
    },
  );

  assert.deepEqual(deleteCalls, [1000, 1]);
  assert.equal(summary.expiredCount, 1001);
  assert.equal(summary.deletedCount, 1);
  assert.equal(summary.deleteErrorCount, 1);
  assert.equal(logErrors.length, 1);
});

test("scheduled delegates to waitUntil", async () => {
  let capturedPromise;
  const ctx = {
    waitUntil(promise) {
      capturedPromise = promise;
    },
  };

  const fakeBucket = {
    async list() {
      return { objects: [], truncated: false };
    },
    async delete() {},
  };

  await worker.scheduled(
    { cron: "*/30 * * * *" },
    {
      R2_BUCKET: fakeBucket,
      R2_CLEANUP_PREFIX: "images",
      R2_CLEANUP_MAX_AGE_SECONDS: "10800",
    },
    ctx,
  );

  assert.ok(capturedPromise);
  await capturedPromise;
});
