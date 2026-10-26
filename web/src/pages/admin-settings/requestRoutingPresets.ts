import type {
  RequestRoute,
  RequestRouteConditions,
  RequestRouteMediaType,
} from "@/api/v2/adminRequests";
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
  /** The card's title, and the rule's name. */
  name: string;
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
  const plural = mediaType === "series" ? "series" : "movies";
  switch (id) {
    case "anime":
      return {
        id,
        name: "Anime",
        description:
          mediaType === "series"
            ? "Japanese animation, and titles TMDB or AniDB list as anime. For Sonarr, also sets the series type to Anime."
            : "Japanese animation, and titles TMDB or AniDB list as anime.",
        conditions: { anime: true },
        noun: `anime ${plural}`,
        matches: `${plural} that are anime`,
      };
    case "foreign": {
      const name = routingLanguageName(language);
      return {
        id,
        name: "Foreign language",
        description: `Titles not originally in ${name}.`,
        conditions: { exclude_original_languages: [language] },
        noun: `foreign-language ${plural}`,
        matches: `${plural} not originally in ${name}`,
      };
    }
    case "kids":
      return {
        id,
        name: "Kids & family",
        description: "Family and kids titles rated PG or lower.",
        conditions: {
          genre_ids: mediaType === "series" ? [FAMILY_GENRE, KIDS_GENRE] : [FAMILY_GENRE],
          max_content_rating: "PG",
        },
        noun: `kids & family ${plural}`,
        matches:
          mediaType === "series"
            ? "family and kids series rated PG or lower"
            : "family movies rated PG or lower",
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
