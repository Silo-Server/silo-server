export interface RecommendationProviderPreset {
  id: string;
  label: string;
  tag?: string;
  description: string;
  baseUrl: string;
  model: string;
  needsToken: boolean;
}

export const RECOMMENDATION_PROVIDER_PRESETS: RecommendationProviderPreset[] = [
  {
    id: "gemini",
    label: "Gemini",
    tag: "Recommended",
    description: "Most accurate. Requires a Google AI API key.",
    baseUrl: "https://generativelanguage.googleapis.com",
    model: "gemini-embedding-001",
    needsToken: true,
  },
  {
    id: "ollama",
    label: "Ollama",
    tag: "Local",
    description: "Free, self-hosted. Needs Ollama running. About 640 MB; 1024-dimension vectors.",
    baseUrl: "http://ollama:11434",
    // The :latest tag is the 8B model, whose 4096-dimension vectors Silo
    // cannot store.
    model: "qwen3-embedding:0.6b",
    needsToken: false,
  },
  {
    id: "openai",
    label: "OpenAI",
    description: "High quality. Requires an OpenAI API key.",
    baseUrl: "https://api.openai.com",
    model: "text-embedding-3-large",
    needsToken: true,
  },
];

export const RECOMMENDATION_CUSTOM_PROVIDER_PRESET: RecommendationProviderPreset = {
  id: "custom",
  label: "Custom",
  description: "Any OpenAI-compatible endpoint.",
  baseUrl: "",
  model: "",
  needsToken: false,
};

export const RECOMMENDATION_PROVIDER_OPTIONS: RecommendationProviderPreset[] = [
  ...RECOMMENDATION_PROVIDER_PRESETS,
  RECOMMENDATION_CUSTOM_PROVIDER_PRESET,
];

export function matchRecommendationProviderPreset(
  baseUrl: string | null | undefined,
  model: string | null | undefined,
): RecommendationProviderPreset | null {
  const normalizedBaseUrl = normalizeBaseUrl(baseUrl);
  const normalizedModel = model?.trim() ?? "";

  if (!normalizedBaseUrl || !normalizedModel) {
    return null;
  }

  return (
    RECOMMENDATION_PROVIDER_PRESETS.find(
      (preset) =>
        normalizeBaseUrl(preset.baseUrl) === normalizedBaseUrl && preset.model === normalizedModel,
    ) ?? null
  );
}

function normalizeBaseUrl(value: string | null | undefined): string {
  return (value ?? "").trim().replace(/\/+$/, "").toLowerCase();
}
