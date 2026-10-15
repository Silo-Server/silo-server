export interface RecFieldDef {
  key: string;
  label: string;
  type: "text" | "number" | "password" | "toggle" | "duration";
  hint?: string;
  defaultValue?: string;
}

export interface RecSectionDef {
  title: string;
  fields: RecFieldDef[];
}

export interface RecommendationEmbeddingLockViewModel {
  model: string;
  sourceDimensions: number;
  storageDimensions: number;
  note: string;
}

const RECOMMENDATIONS_GENERAL_SECTION: RecSectionDef = {
  title: "General",
  fields: [{ key: "recommendations.enabled", label: "Enable Recommendations", type: "toggle" }],
};

const RECOMMENDATIONS_EMBEDDING_SECTION: RecSectionDef = {
  title: "Embedding Configuration",
  fields: [
    {
      key: "recommendations.embedding_base_url",
      label: "Base URL",
      type: "text",
      hint: "e.g. http://ollama:11434",
    },
    {
      key: "recommendations.embedding_model",
      label: "Model",
      type: "text",
      hint: "e.g. text-embedding-3-large",
    },
    {
      key: "recommendations.embedding_auth_token",
      label: "Auth Token",
      type: "password",
      hint: "Optional bearer token",
    },
  ],
};

const RECOMMENDATIONS_SCHEDULE_SECTION: RecSectionDef = {
  title: "Schedule",
  fields: [
    {
      key: "recommendations.embeddings_cron",
      label: "Embeddings Cron",
      type: "text",
      hint: "Cron expression",
      defaultValue: "0 3 * * *",
    },
    {
      key: "recommendations.taste_profiles_cron",
      label: "Taste Profiles Cron",
      type: "text",
      hint: "Cron expression",
      defaultValue: "0 4 * * *",
    },
    {
      key: "recommendations.cowatch_cron",
      label: "Co-Watch Cron",
      type: "text",
      hint: "Cron expression",
      defaultValue: "30 4 * * *",
    },
    {
      key: "recommendations.recommendations_cron",
      label: "Recommendations Cron",
      type: "text",
      hint: "Cron expression",
      defaultValue: "0 5 * * *",
    },
  ],
};

const RECOMMENDATIONS_ADVANCED_SECTION: RecSectionDef = {
  title: "Advanced",
  fields: [
    {
      key: "recommendations.taste_decay_half_life_days",
      label: "Time Decay Half-Life (days)",
      type: "number",
      hint: "How fast old signals lose weight",
      defaultValue: "180",
    },
    {
      key: "recommendations.diversity_lambda",
      label: "Diversity Lambda",
      type: "text",
      hint: "0 = max diversity, 1 = max relevance",
      defaultValue: "0.7",
    },
  ],
};

const RECOMMENDATIONS_SECTIONS: RecSectionDef[] = [
  RECOMMENDATIONS_GENERAL_SECTION,
  RECOMMENDATIONS_EMBEDDING_SECTION,
  RECOMMENDATIONS_SCHEDULE_SECTION,
  RECOMMENDATIONS_ADVANCED_SECTION,
];

const EMBEDDING_LOCK_NOTE =
  "Switching the embedding base URL or model requires resetting embeddings, which deletes them all.";

export function buildRecommendationSections(): RecSectionDef[] {
  return RECOMMENDATIONS_SECTIONS;
}

export function getAllRecommendationFields(): RecFieldDef[] {
  return RECOMMENDATIONS_SECTIONS.flatMap((section) => section.fields);
}

export function parseRecommendationEmbeddingLock(
  raw: string | undefined | null,
): RecommendationEmbeddingLockViewModel | null {
  if (!raw) {
    return null;
  }

  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }

  if (typeof parsed !== "object" || parsed === null) {
    return null;
  }

  const record = parsed as Record<string, unknown>;
  const model = typeof record.model === "string" ? record.model.trim() : "";
  const sourceDimensions =
    typeof record.source_dimensions === "number" ? record.source_dimensions : null;
  const storageDimensions =
    typeof record.storage_dimensions === "number" ? record.storage_dimensions : null;

  if (!model || sourceDimensions === null) {
    return null;
  }

  return {
    model,
    sourceDimensions,
    storageDimensions: storageDimensions ?? 3072,
    note: EMBEDDING_LOCK_NOTE,
  };
}
