// @vitest-environment node

import { describe, expect, it } from "vitest";

import { parseRecommendationEmbeddingLock } from "./recommendationsSettings";

describe("parseRecommendationEmbeddingLock", () => {
  it("parses lock metadata for display", () => {
    const lock = parseRecommendationEmbeddingLock(
      JSON.stringify({
        model: "text-embedding-3-large",
        source_dimensions: 1536,
        storage_dimensions: 3072,
      }),
    );

    expect(lock).toEqual({
      model: "text-embedding-3-large",
      sourceDimensions: 1536,
      storageDimensions: 3072,
      note: expect.stringContaining("resetting embeddings"),
    });
  });

  it("returns null for malformed input", () => {
    expect(parseRecommendationEmbeddingLock("not json")).toBeNull();
  });
});
