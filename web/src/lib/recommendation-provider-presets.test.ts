// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  RECOMMENDATION_PROVIDER_OPTIONS,
  matchRecommendationProviderPreset,
} from "./recommendation-provider-presets";

describe("recommendation provider presets", () => {
  it("includes the wizard custom option alongside the built-in providers", () => {
    expect(RECOMMENDATION_PROVIDER_OPTIONS.map((preset) => preset.id)).toEqual([
      "gemini",
      "ollama",
      "openai",
      "custom",
    ]);
  });

  it("matches saved embedding settings back to a built-in provider", () => {
    expect(
      matchRecommendationProviderPreset("https://api.openai.com/", "text-embedding-3-large")?.id,
    ).toBe("openai");
  });

  it("uses an Ollama model whose vectors Silo can store", () => {
    const ollama = RECOMMENDATION_PROVIDER_OPTIONS.find((preset) => preset.id === "ollama");
    expect(ollama?.model).toBe("qwen3-embedding:0.6b");
    expect(
      matchRecommendationProviderPreset("http://ollama:11434", "qwen3-embedding:0.6b")?.id,
    ).toBe("ollama");
    // Servers saved with the 4096-dimension :latest tag show as a custom model.
    expect(
      matchRecommendationProviderPreset("http://ollama:11434", "qwen3-embedding:latest"),
    ).toBeNull();
  });

  it("returns null when the settings do not match a built-in provider", () => {
    expect(matchRecommendationProviderPreset("http://localhost:9999", "custom-model")).toBeNull();
  });
});
