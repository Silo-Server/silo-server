/**
 * Variants: the ready-made presets one row kind offers ("Trending on this
 * server" over 24 hours, 7 days or 30 days). Each family names the config keys
 * that tell its presets apart (its discriminator keys). Inferring a variant
 * is strict: a config that does not match a preset exactly has no variant,
 * and the form shows none selected instead of guessing. Applying a variant
 * changes only the discriminator keys and keeps every other key.
 */
import { t, type MessageKey } from "@/i18n";
import { NOT_OFFERED_FOR_NEW_ROWS, retiredKindLabel, rowKindLabel } from "./catalog";

type Config = Record<string, unknown>;

export interface VariantOption {
  presetKey: string;
  /** The short name on a picker chip ("7 days"). */
  chip: MessageKey;
  /** The radio card's name ("Last 7 days"). */
  label: MessageKey;
  hint?: MessageKey;
  /** The discriminator values this preset stores. */
  values: Config;
}

export interface VariantFamily {
  /** The radio group's label ("Time window"). */
  control: MessageKey;
  /** Row name help: "Follows {follows} until you type your own name." */
  follows: MessageKey;
  keys: readonly string[];
  options: readonly VariantOption[];
  infer?: (config: Config) => string | null;
  apply?: (config: Config, option: VariantOption) => Config;
}

const HOLIDAY_THEMES = [
  "halloween",
  "christmas",
  "new_year",
  "valentines",
  "st_patricks",
  "easter",
  "thanksgiving",
  "summer_blockbuster",
  "saturday_morning",
];

/** The holidays a seasonal row can show, by their config key. */
export const SEASONAL_THEME_LABELS: Readonly<Record<string, MessageKey>> = {
  valentines: "web.home.rows.variants.themes.valentines",
  st_patricks: "web.home.rows.variants.themes.stPatricks",
  easter: "web.home.rows.variants.themes.easter",
  thanksgiving: "web.home.rows.variants.themes.thanksgiving",
  new_year: "web.home.rows.variants.themes.newYear",
  christmas: "web.home.rows.variants.themes.christmas",
  halloween: "web.home.rows.variants.themes.halloween",
  saturday_morning: "web.home.rows.variants.themes.saturdayMorning",
  family_movie_night: "web.home.rows.variants.themes.familyMovieNight",
  summer_blockbuster: "web.home.rows.variants.themes.summerBlockbuster",
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
  // Only Holidays shows custom holidays, so a row with nothing else is Holidays.
  if (
    themes === undefined &&
    typeof config.theme !== "string" &&
    Array.isArray(config.custom_holidays)
  ) {
    return "se_auto";
  }
  return null;
}

function applySeasonal(config: Config, option: VariantOption): Config {
  const { theme: _legacy, ...rest } = config;
  // Custom holidays outrank every theme and only the Holidays variant shows
  // them or the holidays' own dates, so a Family movie night row must not keep
  // any out of sight.
  const { custom_holidays: _custom, theme_dates: _dates, ...familyNight } = rest;
  const base = option.presetKey === "se_family_movie_night" ? familyNight : rest;
  return { ...base, enabled_themes: [...(option.values.enabled_themes as string[])] };
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
    control: "web.home.rows.variants.controls.kind",
    follows: "web.home.rows.variants.follows.kind",
    keys: ["continue_type"],
    options: [
      {
        presetKey: "continue_watching_default",
        chip: "web.home.rows.sections.watching",
        label: "web.home.rows.sections.watching",
        hint: "web.home.rows.variants.options.watchingHint",
        values: { continue_type: "watching" },
      },
      {
        presetKey: "continue_listening_default",
        chip: "web.home.rows.sections.listening",
        label: "web.home.rows.sections.listening",
        hint: "web.home.rows.variants.options.listeningHint",
        values: { continue_type: "listening" },
      },
    ],
  },
  trending_on_server: {
    control: "web.home.rows.variants.controls.timeWindow",
    follows: "web.home.rows.variants.follows.timeWindow",
    keys: ["window"],
    options: [
      {
        presetKey: "tr_24h",
        chip: "web.home.rows.variants.options.hours24",
        label: "web.home.rows.variants.options.last24Hours",
        hint: "web.home.rows.variants.options.hotToday",
        values: { window: "24h" },
      },
      {
        presetKey: "tr_7d",
        chip: "web.home.rows.variants.options.days7",
        label: "web.home.rows.variants.options.last7Days",
        hint: "web.home.rows.variants.options.weeklyMix",
        values: { window: "7d" },
      },
      {
        presetKey: "tr_30d",
        chip: "web.home.rows.variants.options.days30",
        label: "web.home.rows.variants.options.last30Days",
        hint: "web.home.rows.variants.options.changesSlowly",
        values: { window: "30d" },
      },
    ],
  },
  most_watched: {
    control: "web.home.rows.variants.controls.period",
    follows: "web.home.rows.variants.follows.period",
    keys: ["window"],
    options: [
      {
        presetKey: "mw_week",
        chip: "web.home.rows.variants.options.thisWeek",
        label: "web.home.rows.variants.options.thisWeek",
        hint: "web.home.rows.variants.options.last7DaysHint",
        values: { window: "week" },
      },
      {
        presetKey: "mw_month",
        chip: "web.home.rows.variants.options.thisMonth",
        label: "web.home.rows.variants.options.thisMonth",
        hint: "web.home.rows.variants.options.last30DaysHint",
        values: { window: "month" },
      },
    ],
  },
  trending_discover: {
    control: "web.home.rows.variants.controls.period",
    follows: "web.home.rows.variants.follows.period",
    keys: ["source", "window"],
    options: [
      {
        presetKey: "tdisc_tmdb_day",
        chip: "web.home.rows.variants.options.today",
        label: "web.home.rows.variants.options.today",
        hint: "web.home.rows.variants.options.changesDaily",
        values: { source: "tmdb", window: "day" },
      },
      {
        presetKey: "tdisc_tmdb_week",
        chip: "web.home.rows.variants.options.thisWeek",
        label: "web.home.rows.variants.options.thisWeek",
        hint: "web.home.rows.variants.options.steadierWeekly",
        values: { source: "tmdb", window: "week" },
      },
    ],
  },
  mood_collection: {
    control: "web.home.rows.variants.controls.mood",
    follows: "web.home.rows.variants.follows.mood",
    keys: ["mood"],
    options: (
      [
        ["mood_feel_good", "web.home.rows.variants.options.feelGood", "feel_good"],
        ["mood_mind_bending", "web.home.rows.variants.options.mindBending", "mind_bending"],
        ["mood_comfort", "web.home.rows.variants.options.comfort", "comfort"],
        ["mood_edge_of_seat", "web.home.rows.variants.options.edgeOfSeat", "edge_of_seat"],
        ["mood_tearjerker", "web.home.rows.variants.options.tearjerker", "tearjerker"],
        ["mood_quiet_sunday", "web.home.rows.variants.options.quietSunday", "quiet_sunday"],
        ["mood_date_night", "web.home.rows.variants.options.dateNight", "date_night"],
        ["mood_after_midnight", "web.home.rows.variants.options.afterMidnight", "after_midnight"],
      ] as const
    ).map(([presetKey, label, mood]) => ({
      presetKey,
      chip: label,
      label,
      values: { mood },
    })),
  },
  seasonal_themed: {
    control: "web.home.rows.variants.controls.theme",
    follows: "web.home.rows.variants.follows.theme",
    keys: ["enabled_themes", "theme"],
    options: [
      {
        presetKey: "se_auto",
        chip: "web.home.rows.variants.options.holidays",
        label: "web.home.rows.variants.options.holidays",
        hint: "web.home.rows.variants.options.holidaysHint",
        values: { enabled_themes: HOLIDAY_THEMES },
      },
      {
        presetKey: "se_family_movie_night",
        chip: "web.home.rows.variants.themes.familyMovieNight",
        label: "web.home.rows.variants.themes.familyMovieNight",
        hint: "web.home.rows.variants.options.familyHint",
        values: { enabled_themes: ["family_movie_night"] },
      },
    ],
    infer: inferSeasonal,
    apply: applySeasonal,
  },
  editorial_spotlight: {
    control: "web.home.rows.variants.controls.spotlightOn",
    follows: "web.home.rows.variants.follows.spotlightOn",
    keys: ["subject_type", "subject"],
    options: [
      {
        presetKey: "es_director_auto",
        chip: "web.home.rows.variants.options.director",
        label: "web.home.rows.variants.options.director",
        hint: "web.home.rows.variants.options.directorHint",
        values: { subject_type: "director" },
      },
      {
        presetKey: "es_actor",
        chip: "web.home.rows.variants.options.actor",
        label: "web.home.rows.variants.options.actor",
        hint: "web.home.rows.variants.options.actorHint",
        values: { subject_type: "actor" },
      },
      {
        presetKey: "es_studio",
        chip: "web.home.rows.variants.options.studio",
        label: "web.home.rows.variants.options.studio",
        hint: "web.home.rows.variants.options.studioHint",
        values: { subject_type: "studio" },
      },
      {
        presetKey: "es_era_80s",
        chip: "web.home.rows.variants.options.eighties",
        label: "web.home.rows.variants.options.eighties",
        hint: "web.home.rows.variants.options.eightiesHint",
        values: { subject_type: "era", subject: "1980s" },
      },
    ],
    infer: inferSpotlight,
    apply: applySpotlight,
  },
  format_showcase: {
    control: "web.home.rows.variants.controls.format",
    follows: "web.home.rows.variants.follows.format",
    keys: ["format", "sort"],
    options: [
      {
        presetKey: "fs_4k",
        chip: "web.home.rows.variants.options.fourK",
        label: "web.home.rows.variants.options.fourK",
        hint: "web.home.rows.variants.options.fourKHint",
        values: { format: "4k" },
      },
      {
        presetKey: "fs_4k_recent",
        chip: "web.home.rows.variants.options.newIn4k",
        label: "web.home.rows.variants.options.newIn4k",
        hint: "web.home.rows.variants.options.newIn4kHint",
        values: { format: "4k", sort: "recent" },
      },
      {
        presetKey: "fs_dv",
        chip: "web.home.rows.variants.options.dolbyVision",
        label: "web.home.rows.variants.options.dolbyVision",
        hint: "web.home.rows.variants.options.dolbyVisionHint",
        values: { format: "dolby_vision" },
      },
      {
        presetKey: "fs_hdr",
        chip: "web.home.rows.variants.options.hdr",
        label: "web.home.rows.variants.options.hdr",
        hint: "web.home.rows.variants.options.hdrHint",
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
    return t("web.home.rows.variants.continueReading");
  }
  const kind = rowKindLabel(sectionType);
  const presetKey = variantOf(sectionType, config);
  const chip = presetKey ? variantOption(sectionType, presetKey)?.chip : undefined;
  if (chip) return t("web.home.rows.variants.kindAndVariant", { kind, variant: t(chip) });
  // A legacy single-theme seasonal row shows only that holiday.
  const holiday =
    sectionType === "seasonal_themed" && typeof config.theme === "string"
      ? SEASONAL_THEME_LABELS[config.theme]
      : undefined;
  return holiday ? t("web.home.rows.variants.kindOneHoliday", { kind, holiday: t(holiday) }) : kind;
}

/** Rows whose variant cannot change: Continue Reading (ebooks, beta), which has no preset. */
export function variantLocked(sectionType: string, config: Config): boolean {
  return sectionType === "continue_watching" && config.continue_type === "reading";
}

const SUMMARY_CHIPS = 3;

/** "24 hours, 7 days or 30 days": a kind's variants as text, for phones. */
export function variantSummary(type: string): string | null {
  const chips = VARIANT_FAMILIES[type]?.options.map((option) => t(option.chip)) ?? [];
  if (chips.length < 2) return null;
  const [first = "", second = "", third = ""] = chips;
  if (chips.length > SUMMARY_CHIPS) {
    return t("web.home.rows.variants.summaryMore", { first, second, third });
  }
  if (chips.length === SUMMARY_CHIPS) {
    return t("web.home.rows.variants.summaryThree", { first, second, third });
  }
  return t("web.home.rows.variants.summaryTwo", { first, second });
}
