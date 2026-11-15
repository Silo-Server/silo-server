import { t, type MessageKey } from "@/i18n";

export interface RecFieldDef {
  key: string;
  label: string;
  type: "text" | "number" | "password" | "toggle" | "duration";
  hint?: string;
  defaultValue?: string;
}

/** A stable id for each section; compare this rather than the title. */
export type RecSectionId = "general" | "embedding" | "schedule" | "advanced";

export interface RecSectionDef {
  id: RecSectionId;
  /** In the interface language; compare `id`, not this. */
  title: string;
  fields: RecFieldDef[];
}

export interface RecommendationEmbeddingLockViewModel {
  model: string;
  sourceDimensions: number;
  storageDimensions: number;
  note: string;
}

// The tables hold catalog keys; the exported builders resolve them when
// called, so the text follows the interface language.
interface RecFieldSpec extends Omit<RecFieldDef, "label" | "hint"> {
  label: MessageKey;
  hint?: MessageKey;
}

interface RecSectionSpec {
  id: RecSectionId;
  title: MessageKey;
  fields: RecFieldSpec[];
}

const RECOMMENDATIONS_GENERAL_SECTION: RecSectionSpec = {
  id: "general",
  title: "web.admin.settings.recommendations.general.title",
  fields: [
    {
      key: "recommendations.enabled",
      label: "web.admin.settings.recommendations.general.enabled",
      type: "toggle",
    },
  ],
};

// The provider is chosen with its own control; the model is listed here so
// the page seeds and saves it like every other field.
const RECOMMENDATIONS_EMBEDDING_SECTION: RecSectionSpec = {
  id: "embedding",
  title: "web.admin.settings.recommendations.embedding.title",
  fields: [
    {
      key: "recommendations.embedding_model",
      label: "web.admin.settings.recommendations.embedding.model",
      type: "text",
    },
  ],
};

const RECOMMENDATIONS_SCHEDULE_SECTION: RecSectionSpec = {
  id: "schedule",
  title: "web.admin.settings.recommendations.schedule.title",
  fields: [
    {
      key: "recommendations.embeddings_cron",
      label: "web.admin.settings.recommendations.schedule.embeddingsCron",
      type: "text",
      hint: "web.admin.settings.recommendations.schedule.cronHint",
      defaultValue: "0 3 * * *",
    },
    {
      key: "recommendations.taste_profiles_cron",
      label: "web.admin.settings.recommendations.schedule.tasteProfilesCron",
      type: "text",
      hint: "web.admin.settings.recommendations.schedule.cronHint",
      defaultValue: "0 4 * * *",
    },
    {
      key: "recommendations.cowatch_cron",
      label: "web.admin.settings.recommendations.schedule.cowatchCron",
      type: "text",
      hint: "web.admin.settings.recommendations.schedule.cronHint",
      defaultValue: "30 4 * * *",
    },
    {
      key: "recommendations.recommendations_cron",
      label: "web.admin.settings.recommendations.schedule.recommendationsCron",
      type: "text",
      hint: "web.admin.settings.recommendations.schedule.cronHint",
      defaultValue: "0 5 * * *",
    },
  ],
};

const RECOMMENDATIONS_ADVANCED_SECTION: RecSectionSpec = {
  id: "advanced",
  title: "web.admin.settings.recommendations.advanced.title",
  fields: [
    {
      key: "recommendations.taste_decay_half_life_days",
      label: "web.admin.settings.recommendations.advanced.halfLife.label",
      type: "number",
      hint: "web.admin.settings.recommendations.advanced.halfLife.hint",
      defaultValue: "180",
    },
    {
      key: "recommendations.diversity_lambda",
      label: "web.admin.settings.recommendations.advanced.diversity.label",
      type: "text",
      hint: "web.admin.settings.recommendations.advanced.diversity.hint",
      defaultValue: "0.7",
    },
  ],
};

const RECOMMENDATIONS_SECTIONS: RecSectionSpec[] = [
  RECOMMENDATIONS_GENERAL_SECTION,
  RECOMMENDATIONS_EMBEDDING_SECTION,
  RECOMMENDATIONS_SCHEDULE_SECTION,
  RECOMMENDATIONS_ADVANCED_SECTION,
];

function resolveField(field: RecFieldSpec): RecFieldDef {
  const { label, hint, ...rest } = field;
  return { ...rest, label: t(label), ...(hint ? { hint: t(hint) } : {}) };
}

export function buildRecommendationSections(): RecSectionDef[] {
  return RECOMMENDATIONS_SECTIONS.map((section) => ({
    id: section.id,
    title: t(section.title),
    fields: section.fields.map(resolveField),
  }));
}

export function getAllRecommendationFields(): RecFieldDef[] {
  return RECOMMENDATIONS_SECTIONS.flatMap((section) => section.fields.map(resolveField));
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
    note: t("web.admin.settings.recommendations.embeddingLockNote"),
  };
}
