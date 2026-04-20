import { describe, expect, it } from "vitest";
import {
  extractImageURLs,
  extractTextContent,
  extractUsageMetadata,
  type TaskDetailResponse,
} from "./client";

describe("task detail extractors", () => {
  it("reads Gemini image URLs from candidates", () => {
    const detail: TaskDetailResponse = {
      id: "task-1",
      object: "image.task",
      model: "gemini-2.5-flash-image",
      status: "succeeded",
      created_at: 1_776_663_103,
      usage_metadata: { total_token_count: 123 },
      candidates: [
        {
          finishReason: "STOP",
          content: {
            parts: [
              { text: "done" },
              {
                inlineData: {
                  mimeType: "image/png",
                  data: "https://img.example/gemini.png",
                },
              },
            ],
          },
        },
      ],
    };

    expect(extractImageURLs(detail)).toEqual(["https://img.example/gemini.png"]);
    expect(extractTextContent(detail)).toBe("done");
    expect(extractUsageMetadata(detail)).toEqual({ total_token_count: 123 });
  });

  it("reads OpenAI image URLs and usage from result", () => {
    const detail: TaskDetailResponse = {
      id: "task-2",
      object: "image.task",
      model: "gpt-image-2",
      status: "succeeded",
      created_at: 1_776_663_103,
      usage_metadata: { legacy: true },
      result: {
        created: 1_776_663_103,
        data: [
          { url: "https://img.example/openai-a.png" },
          { url: "https://img.example/openai-b.png" },
        ],
        usage: { total_tokens: 2048 },
      },
    };

    expect(extractImageURLs(detail)).toEqual([
      "https://img.example/openai-a.png",
      "https://img.example/openai-b.png",
    ]);
    expect(extractTextContent(detail)).toBe("");
    expect(extractUsageMetadata(detail)).toEqual({ total_tokens: 2048 });
  });
});
