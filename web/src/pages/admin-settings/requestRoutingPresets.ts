import type {
  RequestRoute,
  RequestRouteConditions,
  RequestRouteMediaType,
} from "@/api/v2/adminRequests";
import { englishText, t } from "@/i18n";
import { ROUTING_LANGUAGES, routingLanguageName } from "@/lib/requestRoutingOptions";

import { cleanConditions } from "./requestRoutingModel";

/** The common rules "Add a rule" starts from. */
export type RulePresetId = "anime" | "foreign" | "kids";

export const RULE_PRESET_IDS: readonly RulePresetId[] = ["anime", "foreign", "kids"];

/** TMDB's Family genre, and series' Kids genre (lib/tmdbGenres). */
const FAMILY_GENRE = 10751;
const KIDS_GENRE = 10762;

/**
 * The language a Foreign-language rule keeps local: the primary subtag of the
 * browser's language when routing knows it, English otherwise.
 */
export function adminLanguage(locale: string | undefined): string {
  const primary = (locale ?? "").split(/[-_]/)[0]?.toLowerCase() ?? "";
  return ROUTING_LANGUAGES.some((language) => language.code === primary) ? primary : "en";
}

export interface RulePreset {
  id: RulePresetId;
  /** The card's title. */
  name: string;
  /** The name a rule added from the preset is saved with: always English, since it is stored data the admin can rename. */
  ruleName: string;
  description: string;
  conditions: RequestRouteConditions;
  /** "anime series", for "Where should anime series go?". */
  noun: string;
  /** "series that are anime", for "Matches: …". */
  matches: string;
}

/** A preset for a media type, in the admin's language where one is needed. */
export function rulePreset(
  id: RulePresetId,
  mediaType: RequestRouteMediaType,
  language: string,
): RulePreset {
  const values = { media_type: mediaType };
  switch (id) {
    case "anime":
      return {
        id,
        name: t("web.admin.settings.requests.presets.anime.name"),
        ruleName: englishText("web.admin.settings.requests.presets.anime.name"),
        description: t("web.admin.settings.requests.presets.anime.description", values),
        conditions: { anime: true },
        noun: t("web.admin.settings.requests.presets.anime.noun", values),
        matches: t("web.admin.settings.requests.presets.anime.matches", values),
      };
    case "foreign": {
      const named = { ...values, language: routingLanguageName(language) };
      return {
        id,
        name: t("web.admin.settings.requests.presets.foreign.name"),
        ruleName: englishText("web.admin.settings.requests.presets.foreign.name"),
        description: t("web.admin.settings.requests.presets.foreign.description", named),
        conditions: { exclude_original_languages: [language] },
        noun: t("web.admin.settings.requests.presets.foreign.noun", values),
        matches: t("web.admin.settings.requests.presets.foreign.matches", named),
      };
    }
    case "kids":
      return {
        id,
        name: t("web.admin.settings.requests.presets.kids.name"),
        ruleName: englishText("web.admin.settings.requests.presets.kids.name"),
        description: t("web.admin.settings.requests.presets.kids.description"),
        conditions: {
          genre_ids: mediaType === "series" ? [FAMILY_GENRE, KIDS_GENRE] : [FAMILY_GENRE],
          max_content_rating: "PG",
        },
        noun: t("web.admin.settings.requests.presets.kids.noun", values),
        matches: t("web.admin.settings.requests.presets.kids.matches", values),
      };
  }
}

function sameConditions(a: RequestRouteConditions, b: RequestRouteConditions): boolean {
  const norm = (c: RequestRouteConditions) => {
    const clean = cleanConditions(c) as Record<string, unknown>;
    return JSON.stringify(
      Object.keys(clean)
        .sort()
        .map((key) => {
          const value = clean[key];
          return [
            key,
            Array.isArray(value)
              ? [...value].map((v) => (typeof v === "string" ? v.toLowerCase() : v)).sort()
              : value,
          ];
        }),
    );
  };
  return norm(a) === norm(b);
}

/** The presets a media type's rules already hold, by their exact conditions. */
export function presetsInUse(
  rules: readonly Pick<RequestRoute, "conditions">[],
  mediaType: RequestRouteMediaType,
  language: string,
): Set<RulePresetId> {
  const used = new Set<RulePresetId>();
  for (const id of RULE_PRESET_IDS) {
    const preset = rulePreset(id, mediaType, language);
    if (rules.some((rule) => sameConditions(rule.conditions, preset.conditions))) used.add(id);
  }
  return used;
}
