// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  buildRecommendationSections,
  parseRecommendationEmbeddingLock,
} from "./recommendationsSettings";

describe("buildRecommendationSections", () => {
  it("uses a single embedding configuration section", () => {
    const sections = buildRecommendationSections();

    expect(sections.map((section) => section.title)).toEqual([
      "General",
      "Embedding Configuration",
      "Schedule",
      "Advanced",
    ]);
    expect(sections[1]?.fields.map((field) => field.key)).toEqual([
      "recommendations.embedding_base_url",
      "recommendations.embedding_model",
      "recommendations.embedding_auth_token",
    ]);
  });
});

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
      note: expect.stringContaining("manual reset"),
    });
  });

  it("returns null for malformed input", () => {
    expect(parseRecommendationEmbeddingLock("not json")).toBeNull();
  });
});
