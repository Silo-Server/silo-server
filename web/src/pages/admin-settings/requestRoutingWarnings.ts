import type { PluginAdminFormField, RequestIntegration } from "@/api/types";
import type {
  RequestRoute,
  RequestRouteConditions,
  RequestRouteMediaType,
} from "@/api/v2/adminRequests";

import {
  cleanConditions,
  decidesTier,
  LIST_CONDITION_FIELDS,
  offersAnimeSeriesType,
  ratingAge,
  type Tier,
} from "./requestRoutingModel";
import { serverKind } from "./requestServerModel";

/** A one-click fix a warning offers. */
export type RoutingWarningFix =
  /** Move the rule directly above another one. */
  | { kind: "move-above"; targetId: string }
  /** Set Sonarr's series type to Anime on the rule's Sonarr destinations. */
  | { kind: "set-anime-series-type" }
  /** Open Everything else's editor. */
  | { kind: "edit-fallback" };

export interface RoutingWarning {
  key: string;
  text: string;
  fix?: RoutingWarningFix;
}

export interface RoutingWarningInput {
  mediaType: RequestRouteMediaType;
  /** The media type's rules in order, Everything else excluded. */
  rules: readonly RequestRoute[];
  fallback: RequestRoute | undefined;
  servers: readonly RequestIntegration[];
  /** The plugin form fields a server's destination can set. */
  serverFields: (server: RequestIntegration) => readonly PluginAdminFormField[];
  /** The admin's language, as the Foreign-language preset uses it. */
  language: string;
  /** "Also request a 4K version of every title" is on (as saved). */
  forceDual: boolean;
}

/* ------------------------------------------------------------------------ */
/* "Never used": does a rule above catch everything a rule below would?     */
/* ------------------------------------------------------------------------ */

type IncludeField = (typeof LIST_CONDITION_FIELDS)[keyof typeof LIST_CONDITION_FIELDS][0];

/** Facts a title has exactly one of: excluding values another rule never wants is a no-op. */
const SINGLE_VALUED: ReadonlySet<IncludeField> = new Set([
  "original_languages",
  "requester_user_ids",
]);

const KNOWN_KEYS = new Set<string>([
  "anime",
  "year_from",
  "year_to",
  "max_content_rating",
  ...Object.values(LIST_CONDITION_FIELDS).flat(),
]);

function values(conditions: RequestRouteConditions, key: string): (string | number)[] {
  const list = (conditions as Record<string, unknown>)[key];
  return Array.isArray(list)
    ? list.map((v) => (typeof v === "string" ? v.toLowerCase() : (v as number)))
    : [];
}

/**
 * Whether every title (and request) that meets `b` also meets `a`. This is
 * conservative: it answers false whenever it cannot tell, so a warning built
 * on it is never wrong.
 */
export function conditionsCover(a: RequestRouteConditions, b: RequestRouteConditions): boolean {
  const ca = cleanConditions(a);
  const cb = cleanConditions(b);
  if (Object.keys(ca).some((key) => !KNOWN_KEYS.has(key))) return false;
  if (ca.anime !== undefined && cb.anime !== ca.anime) return false;
  for (const [include, exclude] of Object.values(LIST_CONDITION_FIELDS)) {
    const wantA = values(ca, include);
    const wantB = values(cb, include);
    if (wantA.length > 0 && (wantB.length === 0 || !wantB.every((v) => wantA.includes(v)))) {
      return false;
    }
    const noneA = values(ca, exclude);
    if (noneA.length > 0) {
      const noneB = values(cb, exclude);
      const excludedToo = noneA.every((v) => noneB.includes(v));
      const neverWanted =
        SINGLE_VALUED.has(include) && wantB.length > 0 && wantB.every((v) => !noneA.includes(v));
      if (!excludedToo && !neverWanted) return false;
    }
  }
  if (ca.year_from && !(cb.year_from && cb.year_from >= ca.year_from)) return false;
  if (ca.year_to && !(cb.year_to && cb.year_to <= ca.year_to)) return false;
  if (ca.max_content_rating) {
    // By each rating's own minimum age, as the server compares them.
    const ageA = ratingAge(ca.max_content_rating);
    const ageB = ratingAge(cb.max_content_rating);
    if (ageA === undefined || ageB === undefined || ageB > ageA) return false;
  }
  return true;
}

const TIERS: readonly Tier[] = ["hd", "uhd"];

/**
 * Whether `above` leaves nothing for `below` to do: both on, `above` catches
 * every request `below` would, and decides every copy `below` decides.
 */
export function ruleShadows(above: RequestRoute, below: RequestRoute): boolean {
  if (!above.enabled || !below.enabled) return false;
  if (!conditionsCover(above.conditions, below.conditions)) return false;
  const decided = TIERS.filter((tier) => decidesTier(below, tier));
  return decided.length > 0 && decided.every((tier) => decidesTier(above, tier));
}

/* ------------------------------------------------------------------------ */
/* Warnings                                                                  */
/* ------------------------------------------------------------------------ */

const KIND_LABEL: Record<string, string> = { radarr: "Radarr", sonarr: "Sonarr" };
const WANTED_KIND: Record<RequestRouteMediaType, string> = { movie: "radarr", series: "sonarr" };

function destinationServers(route: RequestRoute): string[] {
  return [
    ...new Set([route.hd.integration_id, route.uhd.integration_id].filter(Boolean)),
  ] as string[];
}

function serverWarnings(
  route: RequestRoute,
  input: RoutingWarningInput,
  waits: string,
): RoutingWarning[] {
  const out: RoutingWarning[] = [];
  const plural = input.mediaType === "series" ? "series" : "movies";
  const wanted = WANTED_KIND[input.mediaType];
  for (const id of destinationServers(route)) {
    const server = input.servers.find((candidate) => candidate.id === id);
    if (!server) {
      out.push({ key: `missing-${id}`, text: "A server this sends to no longer exists." });
      continue;
    }
    const kind = serverKind(server);
    if (KIND_LABEL[kind] && kind !== wanted) {
      out.push({
        key: `kind-${id}`,
        text: `${server.name} is a ${KIND_LABEL[kind]} server; ${plural} need ${KIND_LABEL[wanted]}.`,
      });
    } else if (!server.enabled) {
      out.push({ key: `off-${id}`, text: `${server.name} is turned off. ${waits}` });
    } else if (!server.installation_id || !server.has_api_key) {
      out.push({ key: `setup-${id}`, text: `${server.name} needs setup.` });
    }
  }
  return out;
}

/** The rule's Sonarr destinations that would add anime as a standard series. */
export function animeSeriesTypeTiers(route: RequestRoute, input: RoutingWarningInput): Tier[] {
  if (input.mediaType !== "series" || route.conditions.anime !== true) return [];
  return TIERS.filter((tier) => {
    const dest = tier === "hd" ? route.hd : route.uhd;
    const server = input.servers.find((candidate) => candidate.id === dest.integration_id);
    if (!server || serverKind(server) !== "sonarr") return false;
    if (!offersAnimeSeriesType(input.serverFields(server))) return false;
    const type = dest.overrides?.series_type ?? server.plugin_config?.series_type;
    return type !== "anime";
  });
}

const LANGUAGE_KEYS: ReadonlySet<string> = new Set([
  "original_languages",
  "exclude_original_languages",
]);

/** A rule above that takes most anime first: one for Japanese, or one against the admin's language. */
function catchesAnimeFirst(above: RequestRoute, anime: RequestRoute, language: string): boolean {
  if (!above.enabled) return false;
  // Only a rule on language alone: one that also wants a requester, years or
  // anything else catches only some anime, too few to say "most".
  const keys = Object.keys(cleanConditions(above.conditions));
  if (keys.length !== 1 || !LANGUAGE_KEYS.has(keys[0]!)) return false;
  const excluded = values(above.conditions, "exclude_original_languages");
  const wanted = values(above.conditions, "original_languages");
  const takesAnime =
    wanted.includes("ja") || (excluded.includes(language) && !excluded.includes("ja"));
  return takesAnime && TIERS.some((tier) => decidesTier(anime, tier) && decidesTier(above, tier));
}

/** The warnings for each route of one media type, by route ID. */
export function routingWarnings(input: RoutingWarningInput): Map<string, RoutingWarning[]> {
  const out = new Map<string, RoutingWarning[]>();
  input.rules.forEach((rule, index) => {
    const warnings: RoutingWarning[] = [];
    const above = input.rules.slice(0, index);
    const shadow = above.find((candidate) => ruleShadows(candidate, rule));
    if (shadow) {
      warnings.push({
        key: "never-used",
        text: `Never used: “${shadow.name}” above catches every request this rule would.`,
        fix: { kind: "move-above", targetId: shadow.id },
      });
    }
    if (rule.enabled && rule.conditions.anime === true) {
      const first = above.find((candidate) => catchesAnimeFirst(candidate, rule, input.language));
      if (first && first.id !== shadow?.id) {
        warnings.push({
          key: "anime-order",
          text: `Anime titles are usually Japanese, so “${first.name}” above catches most of them first.`,
          fix: { kind: "move-above", targetId: first.id },
        });
      }
    }
    warnings.push(
      ...serverWarnings(
        rule,
        input,
        "Requests this rule sends there can't go through until it's back on.",
      ),
    );
    if (animeSeriesTypeTiers(rule, input).length > 0) {
      warnings.push({
        key: "anime-series-type",
        text: "Sonarr will add these as standard series.",
        fix: { kind: "set-anime-series-type" },
      });
    }
    if (warnings.length > 0) out.set(rule.id, warnings);
  });

  const fallback = input.fallback;
  if (fallback?.hd.integration_id) {
    const warnings = serverWarnings(
      fallback,
      input,
      "Requests sent there can't go through until it's back on.",
    );
    if (input.forceDual && !fallback.uhd.integration_id) {
      warnings.push({
        key: "force-dual",
        text: "“Also request a 4K version of every title” is on, but Everything else doesn't send 4K versions. Titles no rule sends to a 4K server get HD only.",
        fix: { kind: "edit-fallback" },
      });
    }
    if (warnings.length > 0) out.set(fallback.id, warnings);
  }
  return out;
}

/** The label of a fix's button. */
export function fixLabel(fix: RoutingWarningFix): string {
  switch (fix.kind) {
    case "move-above":
      return "Move above";
    case "set-anime-series-type":
      return "Set series type to Anime";
    case "edit-fallback":
      return "Choose a 4K server";
  }
}
