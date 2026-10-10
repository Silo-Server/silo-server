/**
 * Variants: the ready-made presets one row kind offers ("Trending on this
 * server" over 24 hours, 7 days or 30 days). Each family names the config keys
 * that tell its presets apart (its discriminator keys). Inferring a variant
 * is strict: a config that does not match a preset exactly has no variant,
 * and the form shows none selected instead of guessing. Applying a variant
 * changes only the discriminator keys and keeps every other key.
 */
import { isTraktConfig } from "@/lib/sectionTypes";
import { NOT_OFFERED_FOR_NEW_ROWS, retiredKindLabel, rowKindLabel } from "./catalog";

type Config = Record<string, unknown>;

export interface VariantOption {
  presetKey: string;
  /** The short name on a picker chip ("7 days"). */
  chip: string;
  /** The radio card's name ("Last 7 days"). */
  label: string;
  hint?: string;
  /** The discriminator values this preset stores. */
  values: Config;
}

export interface VariantFamily {
  /** The radio group's label ("Time window"). */
  control: string;
  /** Row name help: "Follows {follows} until you type your own name." */
  follows: string;
  keys: readonly string[];
  options: readonly VariantOption[];
  infer?: (config: Config) => string | null;
  apply?: (config: Config, option: VariantOption) => Config;
}

const HOLIDAY_THEMES = [
  "halloween",
  "christmas",
  "valentines",
  "st_patricks",
  "thanksgiving",
  "summer_blockbuster",
  "saturday_morning",
];

/** The holidays a seasonal row can show, by their config key. */
export const SEASONAL_THEME_LABELS: Readonly<Record<string, string>> = {
  valentines: "Valentine's Day",
  st_patricks: "St. Patrick's Day",
  thanksgiving: "Thanksgiving",
  christmas: "Christmas",
  halloween: "Halloween",
  saturday_morning: "Saturday morning cartoons",
  family_movie_night: "Family movie night",
  summer_blockbuster: "Summer blockbusters",
};

/** Spotlight presets that rotate start weekly, as the catalog's presets do. */
const SPOTLIGHT_ROTATION: Config = { auto_rotate: true, rotation_cadence: "weekly" };

function blank(value: unknown): unknown {
  return value === undefined || value === null || value === "" ? undefined : value;
}

function sameValue(a: unknown, b: unknown): boolean {
  return JSON.stringify(blank(a)) === JSON.stringify(blank(b));
}

function inferSeasonal(config: Config): string | null {
  const themes = config.enabled_themes;
  if (Array.isArray(themes)) {
    return themes.length === 1 && themes[0] === "family_movie_night"
      ? "se_family_movie_night"
      : "se_auto";
  }
  if (themes === undefined && config.theme === "family_movie_night") return "se_family_movie_night";
  return null;
}

function applySeasonal(config: Config, option: VariantOption): Config {
  const { theme: _legacy, ...rest } = config;
  return { ...rest, enabled_themes: [...(option.values.enabled_themes as string[])] };
}

function inferSpotlight(config: Config): string | null {
  switch (config.subject_type) {
    case "director":
      return "es_director_auto";
    case "actor":
      return "es_actor";
    case "studio":
      return "es_studio";
    case "era":
      return config.subject === "1980s" ? "es_era_80s" : null;
    default:
      return null;
  }
}

function applySpotlight(config: Config, option: VariantOption): Config {
  const { subject: _subject, ...rest } = config;
  if (option.presetKey === "es_era_80s") {
    const { auto_rotate: _rotate, rotation_cadence: _cadence, ...pinned } = rest;
    return { ...pinned, ...option.values };
  }
  // Between people and studios a rotation carries over. A pinned row (era,
  // person or studio) loses its subject here, and the server needs one unless
  // the row rotates, so it takes the preset's rotation.
  const rotatingPerson =
    ["director", "actor", "studio"].includes(String(config.subject_type)) &&
    config.auto_rotate === true;
  return rotatingPerson
    ? { ...rest, subject_type: option.values.subject_type }
    : { ...rest, ...SPOTLIGHT_ROTATION, subject_type: option.values.subject_type };
}

export const VARIANT_FAMILIES: Readonly<Record<string, VariantFamily>> = {
  continue_watching: {
    control: "Kind",
    follows: "the kind",
    keys: ["continue_type"],
    options: [
      {
        presetKey: "continue_watching_default",
        chip: "Watching",
        label: "Watching",
        hint: "Movies and episodes",
        values: { continue_type: "watching" },
      },
      {
        presetKey: "continue_listening_default",
        chip: "Listening",
        label: "Listening",
        hint: "Audiobooks",
        values: { continue_type: "listening" },
      },
    ],
  },
  trending_on_server: {
    control: "Time window",
    follows: "the time window",
    keys: ["window"],
    options: [
      {
        presetKey: "tr_24h",
        chip: "24 hours",
        label: "Last 24 hours",
        hint: "What's hot today",
        values: { window: "24h" },
      },
      {
        presetKey: "tr_7d",
        chip: "7 days",
        label: "Last 7 days",
        hint: "A steady weekly mix",
        values: { window: "7d" },
      },
      {
        presetKey: "tr_30d",
        chip: "30 days",
        label: "Last 30 days",
        hint: "Changes slowly",
        values: { window: "30d" },
      },
    ],
  },
  most_watched: {
    control: "Period",
    follows: "the period",
    keys: ["window"],
    options: [
      {
        presetKey: "mw_week",
        chip: "This week",
        label: "This week",
        hint: "The last 7 days",
        values: { window: "week" },
      },
      {
        presetKey: "mw_month",
        chip: "This month",
        label: "This month",
        hint: "The last 30 days",
        values: { window: "month" },
      },
    ],
  },
  trending_discover: {
    control: "Period",
    follows: "the period",
    keys: ["source", "window"],
    options: [
      {
        presetKey: "tdisc_tmdb_day",
        chip: "Today",
        label: "Today",
        hint: "Changes every day",
        values: { source: "tmdb", window: "day" },
      },
      {
        presetKey: "tdisc_tmdb_week",
        chip: "This week",
        label: "This week",
        hint: "A steadier weekly list",
        values: { source: "tmdb", window: "week" },
      },
    ],
  },
  mood_collection: {
    control: "Mood",
    follows: "the mood",
    keys: ["mood"],
    options: [
      ["mood_feel_good", "Feel-good", "feel_good"],
      ["mood_mind_bending", "Mind-bending", "mind_bending"],
      ["mood_comfort", "Comfort rewatches", "comfort"],
      ["mood_edge_of_seat", "Edge of your seat", "edge_of_seat"],
      ["mood_tearjerker", "Tearjerkers", "tearjerker"],
      ["mood_quiet_sunday", "Quiet Sunday", "quiet_sunday"],
      ["mood_date_night", "Date night", "date_night"],
      ["mood_after_midnight", "After midnight", "after_midnight"],
    ].map(([presetKey, label, mood]) => ({
      presetKey: presetKey!,
      chip: label!,
      label: label!,
      values: { mood },
    })),
  },
  seasonal_themed: {
    control: "Theme",
    follows: "the theme",
    keys: ["enabled_themes", "theme"],
    options: [
      {
        presetKey: "se_auto",
        chip: "Holidays",
        label: "Holidays",
        hint: "Changes with the calendar",
        values: { enabled_themes: HOLIDAY_THEMES },
      },
      {
        presetKey: "se_family_movie_night",
        chip: "Family movie night",
        label: "Family movie night",
        hint: "Friday and Saturday evenings",
        values: { enabled_themes: ["family_movie_night"] },
      },
    ],
    infer: inferSeasonal,
    apply: applySeasonal,
  },
  editorial_spotlight: {
    control: "Spotlight on",
    follows: "the spotlight",
    keys: ["subject_type", "subject"],
    options: [
      {
        presetKey: "es_director_auto",
        chip: "Director",
        label: "Director",
        hint: "Films by one director",
        values: { subject_type: "director" },
      },
      {
        presetKey: "es_actor",
        chip: "Actor",
        label: "Actor",
        hint: "Titles with one actor",
        values: { subject_type: "actor" },
      },
      {
        presetKey: "es_studio",
        chip: "Studio",
        label: "Studio",
        hint: "Titles from one studio",
        values: { subject_type: "studio" },
      },
      {
        presetKey: "es_era_80s",
        chip: "The 80s",
        label: "The 80s",
        hint: "Films from the 1980s",
        values: { subject_type: "era", subject: "1980s" },
      },
    ],
    infer: inferSpotlight,
    apply: applySpotlight,
  },
  format_showcase: {
    control: "Format",
    follows: "the format",
    keys: ["format", "sort"],
    options: [
      {
        presetKey: "fs_4k",
        chip: "4K",
        label: "4K",
        hint: "Everything in 4K",
        values: { format: "4k" },
      },
      {
        presetKey: "fs_4k_recent",
        chip: "New in 4K",
        label: "New in 4K",
        hint: "The latest 4K additions",
        values: { format: "4k", sort: "recent" },
      },
      {
        presetKey: "fs_dv",
        chip: "Dolby Vision",
        label: "Dolby Vision",
        hint: "Dolby Vision titles",
        values: { format: "dolby_vision" },
      },
      {
        presetKey: "fs_hdr",
        chip: "HDR",
        label: "HDR",
        hint: "HDR-mastered titles",
        values: { format: "hdr" },
      },
    ],
  },
};

export function variantFamily(sectionType: string): VariantFamily | undefined {
  return VARIANT_FAMILIES[sectionType];
}

/** The preset key a row's config matches exactly, or null. Never a fallback. */
export function variantOf(sectionType: string, config: Config): string | null {
  const family = VARIANT_FAMILIES[sectionType];
  if (!family) return null;
  if (family.infer) return family.infer(config);
  const match = family.options.find((option) =>
    family.keys.every((key) => sameValue(config[key], option.values[key])),
  );
  return match?.presetKey ?? null;
}

/**
 * Switches a row's config to another preset of its kind. Only the family's
 * discriminator keys change; library filters, generated-row metadata and the
 * viewer's own tuning stay as they are.
 */
export function applyVariant(sectionType: string, config: Config, presetKey: string): Config {
  const family = VARIANT_FAMILIES[sectionType];
  const option = family?.options.find((entry) => entry.presetKey === presetKey);
  if (!family || !option) return config;
  if (family.apply) return family.apply(config, option);
  const next = { ...config };
  for (const key of family.keys) delete next[key];
  return { ...next, ...option.values };
}

export function variantOption(sectionType: string, presetKey: string): VariantOption | undefined {
  return VARIANT_FAMILIES[sectionType]?.options.find((option) => option.presetKey === presetKey);
}

/**
 * The name on Edit row's "Shows" line: the kind, plus its variant when the
 * config matches one ("Trending worldwide · This week").
 */
export function showsLabel(sectionType: string, config: Config): string {
  if (NOT_OFFERED_FOR_NEW_ROWS.has(sectionType)) return retiredKindLabel(sectionType, config);
  if (sectionType === "continue_watching" && config.continue_type === "reading") {
    return "Continue Reading";
  }
  const presetKey = variantOf(sectionType, config);
  const chip = presetKey ? variantOption(sectionType, presetKey)?.chip : undefined;
  if (chip) return `${rowKindLabel(sectionType)} · ${chip}`;
  // A legacy single-theme seasonal row shows only that holiday.
  const holiday =
    sectionType === "seasonal_themed" && typeof config.theme === "string"
      ? SEASONAL_THEME_LABELS[config.theme]
      : undefined;
  return holiday ? `${rowKindLabel(sectionType)} (${holiday} only)` : rowKindLabel(sectionType);
}

/**
 * Rows whose kind cannot change: the server refuses config changes on legacy
 * Trakt rows.
 */
export function kindLocked(config: Config): boolean {
  return isTraktConfig(config);
}

/**
 * Rows whose variant cannot change: legacy Trakt rows, and Continue Reading
 * (ebooks, beta), which has no preset.
 */
export function variantLocked(sectionType: string, config: Config): boolean {
  return (
    kindLocked(config) ||
    (sectionType === "continue_watching" && config.continue_type === "reading")
  );
}

const SUMMARY_CHIPS = 3;

/** "24 hours, 7 days or 30 days": a kind's variants as text, for phones. */
export function variantSummary(type: string): string | null {
  const chips = VARIANT_FAMILIES[type]?.options.map((option) => option.chip) ?? [];
  if (chips.length < 2) return null;
  if (chips.length > SUMMARY_CHIPS) return `${chips.slice(0, SUMMARY_CHIPS).join(", ")} and more`;
  return `${chips.slice(0, -1).join(", ")} or ${chips.at(-1)}`;
}
