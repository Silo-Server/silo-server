import type { PluginAdminForm, PluginAdminFormField, RequestIntegration } from "@/api/types";
import type {
  RequestRoute,
  RequestRouteBody,
  RequestRouteConditions,
  RequestRouteDestination,
  RequestRouteFacts,
  RequestRouteMediaType,
  RequestRoutePreviewRule,
} from "@/api/v2/adminRequests";
import { evaluateShowWhen } from "@/components/admin/plugins/schemaFormUtils";
import { routingCountryName, routingLanguageName } from "@/lib/requestRoutingOptions";
import { tmdbGenreName } from "@/lib/tmdbGenres";

import {
  ROUTING_OWNED_CONFIG_KEYS,
  SERVICE_KIND_KEY,
  serverConfigSchema,
  serverInstallation,
  type RequestRouterInstallation,
} from "./requestServerModel";

/* ------------------------------------------------------------------------ */
/* Drafts and wire bodies                                                    */
/* ------------------------------------------------------------------------ */

/** One tier's destination while it is being edited. */
export interface RouteDestinationDraft {
  integration_id: string;
  overrides: Record<string, unknown>;
}

export function destinationDraft(dest: RequestRouteDestination | undefined): RouteDestinationDraft {
  return { integration_id: dest?.integration_id ?? "", overrides: { ...(dest?.overrides ?? {}) } };
}

/** The wire form of a destination: nothing at all when no server is chosen. */
export function destinationBody(draft: RouteDestinationDraft): RequestRouteDestination {
  if (!draft.integration_id) return {};
  const overrides = Object.fromEntries(
    Object.entries(draft.overrides).filter(([, value]) => value !== undefined),
  );
  return Object.keys(overrides).length > 0
    ? { integration_id: draft.integration_id, overrides }
    : { integration_id: draft.integration_id };
}

/** A media type's Everything else while it is being edited. */
export interface FallbackDraft {
  hd: RouteDestinationDraft;
  uhd: RouteDestinationDraft;
}

/** Everything else's body: no conditions, always on, and no 4K server means no 4K copy. */
export function fallbackBody(route: RequestRoute, draft: FallbackDraft): RequestRouteBody {
  return {
    name: route.name,
    enabled: true,
    conditions: {},
    hd: destinationBody(draft.hd),
    uhd: destinationBody(draft.uhd),
    skip_uhd: false,
  };
}

/** A tier's destination as an editor holds it: a server, or (4K) no copy. */
export interface DestinationChoice {
  dest: RouteDestinationDraft;
  skip: boolean;
}

/** A rule's two destinations as its editor starts them. */
export function initialChoices(source: RequestRoute | null): {
  hd: DestinationChoice;
  uhd: DestinationChoice;
} {
  return {
    hd: { dest: destinationDraft(source?.hd), skip: false },
    uhd: { dest: destinationDraft(source?.uhd), skip: source?.skip_uhd ?? false },
  };
}

/** The caption under the 4K version. */
export function fourKCaption(forceDual: boolean): string {
  return forceDual
    ? "Sent along with the HD version for every request (General)."
    : "Sent along with the HD version when the requester's playback limit allows 4K.";
}

/** A routing rule while it is being edited. */
export interface RouteRuleDraft {
  name: string;
  enabled: boolean;
  conditions: RequestRouteConditions;
  hd: RouteDestinationDraft;
  uhd: RouteDestinationDraft;
  skipUhd: boolean;
}

export function ruleDraft(route: RequestRoute | null): RouteRuleDraft {
  return {
    name: route?.name ?? "",
    enabled: route?.enabled ?? true,
    conditions: { ...(route?.conditions ?? {}) },
    hd: destinationDraft(route?.hd),
    uhd: destinationDraft(route?.uhd),
    skipUhd: route?.skip_uhd ?? false,
  };
}

/** The list conditions, each an include field and its exclude twin. */
export const LIST_CONDITION_FIELDS = {
  genre: ["genre_ids", "exclude_genre_ids"],
  language: ["original_languages", "exclude_original_languages"],
  country: ["origin_countries", "exclude_origin_countries"],
  network: ["network_ids", "exclude_network_ids"],
  studio: ["company_ids", "exclude_company_ids"],
  requester: ["requester_user_ids", "exclude_requester_user_ids"],
  keyword: ["keyword_ids", "exclude_keyword_ids"],
} as const satisfies Record<
  string,
  readonly [keyof RequestRouteConditions, keyof RequestRouteConditions]
>;

export type ListConditionKind = keyof typeof LIST_CONDITION_FIELDS;
type ListField = (typeof LIST_CONDITION_FIELDS)[ListConditionKind][number];

const STRING_LISTS: ReadonlySet<ListField> = new Set([
  "original_languages",
  "exclude_original_languages",
  "origin_countries",
  "exclude_origin_countries",
]);

const LIST_FIELDS: readonly ListField[] = Object.values(LIST_CONDITION_FIELDS).flat();

/** Drops unset conditions so the body carries only the ones that narrow. */
export function cleanConditions(conditions: RequestRouteConditions): RequestRouteConditions {
  const out: RequestRouteConditions = {};
  if (conditions.anime !== undefined) out.anime = conditions.anime;
  for (const key of LIST_FIELDS) {
    const values = conditions[key] as readonly (string | number)[] | undefined;
    if (values && values.length > 0) (out as Record<string, unknown>)[key] = [...values];
  }
  if (conditions.year_from) out.year_from = conditions.year_from;
  if (conditions.year_to) out.year_to = conditions.year_to;
  if (conditions.max_content_rating) out.max_content_rating = conditions.max_content_rating;
  return out;
}

export function ruleBody(
  draft: RouteRuleDraft,
  mediaType?: RequestRouteMediaType,
): RequestRouteBody {
  return {
    ...(mediaType ? { media_type: mediaType } : {}),
    name: draft.name.trim(),
    enabled: draft.enabled,
    conditions: cleanConditions(draft.conditions),
    hd: destinationBody(draft.hd),
    uhd: draft.skipUhd ? {} : destinationBody(draft.uhd),
    skip_uhd: draft.skipUhd,
  };
}

/** A saved rule's body, e.g. to flip its switch without opening it. */
export function routeBody(route: RequestRoute): RequestRouteBody {
  return ruleBody(ruleDraft(route));
}

/* ------------------------------------------------------------------------ */
/* Condition rows: what the rule editor shows                                */
/* ------------------------------------------------------------------------ */

export type ConditionKind = "anime" | "year" | "rating" | ListConditionKind;

/**
 * One line of "Which requests". A list line holds its values as strings (IDs
 * too) and says whether a title must have any of them or none of them.
 */
export type ConditionRow =
  | { kind: "anime"; anime: boolean }
  | { kind: "year"; from?: number; to?: number }
  | { kind: "rating"; max: string }
  | { kind: ListConditionKind; mode: "any" | "none"; values: string[] };

export type ListConditionRow = Extract<ConditionRow, { mode: "any" | "none" }>;

/** The order conditions are listed and read in. */
const ROW_ORDER: readonly ConditionKind[] = [
  "anime",
  "genre",
  "language",
  "country",
  "year",
  "rating",
  "network",
  "studio",
  "requester",
  "keyword",
];

export function isListRow(row: ConditionRow): row is ListConditionRow {
  return "mode" in row;
}

/** The rows a rule's saved conditions read as, one per set field. */
export function conditionRows(conditions: RequestRouteConditions): ConditionRow[] {
  const rows: ConditionRow[] = [];
  for (const kind of ROW_ORDER) {
    if (kind === "anime") {
      if (conditions.anime !== undefined) rows.push({ kind, anime: conditions.anime });
    } else if (kind === "year") {
      if (conditions.year_from || conditions.year_to) {
        rows.push({
          kind,
          from: conditions.year_from || undefined,
          to: conditions.year_to || undefined,
        });
      }
    } else if (kind === "rating") {
      if (conditions.max_content_rating) rows.push({ kind, max: conditions.max_content_rating });
    } else {
      const [include, exclude] = LIST_CONDITION_FIELDS[kind];
      for (const [field, mode] of [
        [include, "any"],
        [exclude, "none"],
      ] as const) {
        const values = conditions[field] as readonly (string | number)[] | undefined;
        if (values?.length) rows.push({ kind, mode, values: values.map(String) });
      }
    }
  }
  return rows;
}

/** A row's wire field, e.g. `exclude_genre_ids`, for its inline error. */
export function rowField(row: ConditionRow): string {
  if (row.kind === "anime") return "anime";
  if (row.kind === "year") return "year_from";
  if (row.kind === "rating") return "max_content_rating";
  const [include, exclude] = LIST_CONDITION_FIELDS[row.kind];
  return row.mode === "any" ? include : exclude;
}

/**
 * The conditions a set of rows saves as. Rows left empty save nothing, and two
 * rows naming the same field save the values of both.
 */
export function rowsToConditions(rows: readonly ConditionRow[]): RequestRouteConditions {
  const out: Record<string, unknown> = {};
  for (const row of rows) {
    if (row.kind === "anime") out.anime = row.anime;
    else if (row.kind === "year") {
      if (row.from) out.year_from = row.from;
      if (row.to) out.year_to = row.to;
    } else if (row.kind === "rating") {
      if (row.max) out.max_content_rating = row.max;
    } else if (row.values.length > 0) {
      const field = rowField(row) as ListField;
      const values = STRING_LISTS.has(field) ? row.values : row.values.map(Number);
      const current = (out[field] as (string | number)[] | undefined) ?? [];
      out[field] = [...new Set([...current, ...values])];
    }
  }
  return out as RequestRouteConditions;
}

/* ------------------------------------------------------------------------ */
/* Sentences                                                                 */
/* ------------------------------------------------------------------------ */

export interface RoutingNames {
  users?: ReadonlyMap<number, string>;
  /** TMDB network names, for series. Network and studio IDs overlap, so each has its own map. */
  networks?: ReadonlyMap<number, string>;
  /** TMDB studio (company) names, for movies. */
  studios?: ReadonlyMap<number, string>;
}

/** "A", "A or B", "A, B or C". */
function orList(values: readonly string[]): string {
  if (values.length <= 1) return values[0] ?? "";
  return `${values.slice(0, -1).join(", ")} or ${values.at(-1)}`;
}

/** "A", "A and B", "A, B and C". */
function andList(values: readonly string[]): string {
  if (values.length <= 1) return values[0] ?? "";
  return `${values.slice(0, -1).join(", ")} and ${values.at(-1)}`;
}

/** "isn't Horror", "is neither Horror nor War", "is none of A, B or C". */
function notOneOf(values: readonly string[], verb = "is"): string {
  if (values.length === 1) return `${verb === "is" ? "isn't" : `${verb} not`} ${values[0]}`;
  if (values.length === 2) return `${verb} neither ${values[0]} nor ${values[1]}`;
  return `${verb} none of ${orList(values)}`;
}

export function yearRangeLabel(from?: number, to?: number): string {
  if (from && to) return from === to ? String(from) : `${from}–${to}`;
  if (from) return `${from} or later`;
  if (to) return `${to} or earlier`;
  return "";
}

function genreNames(ids: readonly number[], mediaType: RequestRouteMediaType): string[] {
  return ids.map((id) => tmdbGenreName(id, mediaType));
}

function brandNames(ids: readonly number[], noun: string, names: RoutingNames): string[] {
  const map = noun.toLowerCase() === "network" ? names.networks : names.studios;
  return ids.map((id) => map?.get(id) ?? `${noun} ${id}`);
}

function userNames(ids: readonly number[], names: RoutingNames): string[] {
  return ids.map((id) => names.users?.get(id) ?? `account ${id}`);
}

/** The clauses of a rule's conditions, in reading order: "is anime", "is rated PG or lower". */
function conditionClauses(
  conditions: RequestRouteConditions,
  mediaType: RequestRouteMediaType,
  names: RoutingNames = {},
): string[] {
  const c = conditions;
  const out: string[] = [];
  if (c.anime === true) out.push("is anime");
  if (c.anime === false) out.push("isn't anime");
  if (c.genre_ids?.length) out.push(`is ${orList(genreNames(c.genre_ids, mediaType))}`);
  if (c.exclude_genre_ids?.length) out.push(notOneOf(genreNames(c.exclude_genre_ids, mediaType)));
  if (c.original_languages?.length) {
    out.push(`is originally in ${orList(c.original_languages.map(routingLanguageName))}`);
  }
  if (c.exclude_original_languages?.length) {
    out.push(
      `isn't originally in ${orList(c.exclude_original_languages.map(routingLanguageName))}`,
    );
  }
  if (c.origin_countries?.length) {
    out.push(`is from ${orList(c.origin_countries.map(routingCountryName))}`);
  }
  if (c.exclude_origin_countries?.length) {
    out.push(`isn't from ${orList(c.exclude_origin_countries.map(routingCountryName))}`);
  }
  const years = yearRangeLabel(c.year_from, c.year_to);
  if (years) out.push(`${mediaType === "series" ? "first aired" : "came out"} in ${years}`);
  if (c.max_content_rating) out.push(`is rated ${c.max_content_rating} or lower`);
  if (c.network_ids?.length)
    out.push(`is on ${orList(brandNames(c.network_ids, "network", names))}`);
  if (c.exclude_network_ids?.length) {
    out.push(`isn't on ${orList(brandNames(c.exclude_network_ids, "network", names))}`);
  }
  if (c.company_ids?.length) {
    out.push(`is made by ${orList(brandNames(c.company_ids, "studio", names))}`);
  }
  if (c.exclude_company_ids?.length) {
    out.push(`isn't made by ${orList(brandNames(c.exclude_company_ids, "studio", names))}`);
  }
  if (c.requester_user_ids?.length) {
    out.push(`is requested by ${orList(userNames(c.requester_user_ids, names))}`);
  }
  if (c.exclude_requester_user_ids?.length) {
    out.push(`isn't requested by ${orList(userNames(c.exclude_requester_user_ids, names))}`);
  }
  if (c.keyword_ids?.length) {
    out.push(`has TMDB keyword ${orList(c.keyword_ids.map(String))}`);
  }
  if (c.exclude_keyword_ids?.length) {
    out.push(`doesn't have TMDB keyword ${orList(c.exclude_keyword_ids.map(String))}`);
  }
  return out;
}

const ARTICLE: Record<RequestRouteMediaType, string> = { movie: "a movie", series: "a series" };

/** "When a series is Family or Kids and is rated PG or lower". */
export function ruleSentence(
  conditions: RequestRouteConditions,
  mediaType: RequestRouteMediaType,
  names: RoutingNames = {},
): string {
  const clauses = conditionClauses(conditions, mediaType, names);
  if (clauses.length === 0) return `Every ${mediaType === "series" ? "series" : "movie"}`;
  return `When ${ARTICLE[mediaType]} ${andList(clauses)}`;
}

/** A short name for a rule, from its conditions: "Anime", "Not English", "Family or Kids, PG or lower". */
export function autoRuleName(
  conditions: RequestRouteConditions,
  mediaType: RequestRouteMediaType,
  names: RoutingNames = {},
): string {
  const c = conditions;
  const parts: string[] = [];
  if (c.anime === true) parts.push("Anime");
  if (c.anime === false) parts.push("Not anime");
  if (c.genre_ids?.length) parts.push(orList(genreNames(c.genre_ids, mediaType)));
  if (c.exclude_genre_ids?.length) {
    parts.push(`Not ${orList(genreNames(c.exclude_genre_ids, mediaType))}`);
  }
  if (c.original_languages?.length)
    parts.push(orList(c.original_languages.map(routingLanguageName)));
  if (c.exclude_original_languages?.length) {
    parts.push(`Not ${orList(c.exclude_original_languages.map(routingLanguageName))}`);
  }
  if (c.origin_countries?.length) parts.push(orList(c.origin_countries.map(routingCountryName)));
  if (c.exclude_origin_countries?.length) {
    parts.push(`Not from ${orList(c.exclude_origin_countries.map(routingCountryName))}`);
  }
  const years = yearRangeLabel(c.year_from, c.year_to);
  if (years) parts.push(years);
  if (c.max_content_rating) parts.push(`${c.max_content_rating} or lower`);
  if (c.network_ids?.length) parts.push(orList(brandNames(c.network_ids, "Network", names)));
  if (c.exclude_network_ids?.length) {
    parts.push(`Not on ${orList(brandNames(c.exclude_network_ids, "Network", names))}`);
  }
  if (c.company_ids?.length) parts.push(orList(brandNames(c.company_ids, "Studio", names)));
  if (c.exclude_company_ids?.length) {
    parts.push(`Not by ${orList(brandNames(c.exclude_company_ids, "Studio", names))}`);
  }
  if (c.requester_user_ids?.length) {
    parts.push(`Requested by ${orList(userNames(c.requester_user_ids, names))}`);
  }
  if (c.exclude_requester_user_ids?.length) {
    parts.push(`Not requested by ${orList(userNames(c.exclude_requester_user_ids, names))}`);
  }
  if (c.keyword_ids?.length) parts.push(`Keyword ${orList(c.keyword_ids.map(String))}`);
  if (c.exclude_keyword_ids?.length) {
    parts.push(`Not keyword ${orList(c.exclude_keyword_ids.map(String))}`);
  }
  const name = parts.join(", ") || "New rule";
  return name.length > 100 ? `${name.slice(0, 99)}…` : name;
}

/* ------------------------------------------------------------------------ */
/* Destinations                                                              */
/* ------------------------------------------------------------------------ */

const OVERRIDE_LABELS: Record<string, string> = {
  root_folder: "Folder",
  quality_profile_id: "Quality",
  tags: "Tags",
  series_type: "Series type",
  minimum_availability: "Minimum availability",
  search_on_add: "Search on add",
  season_folder: "Season folder",
};

/** The overrides shown on a destination's own rows; the rest go under More settings. */
const INLINE_OVERRIDES: readonly string[] = ["root_folder", "quality_profile_id", "tags"];

/**
 * Overrides whose value already reads as a name. A root folder's option label
 * adds the free space to the path, which is noise in a summary.
 */
const SELF_NAMED_OVERRIDES = new Set(["root_folder"]);

/**
 * Override keys whose values are IDs on the server (a quality profile, tags),
 * so naming them takes the server's options.
 */
export const SERVER_NAMED_OVERRIDES: ReadonlySet<string> = new Set(["quality_profile_id", "tags"]);

export interface OverrideNames {
  /** The server's loaded options (root folders, quality profiles, tags), by field key. */
  options?: Readonly<Record<string, readonly { value: string; label: string }[]>>;
  /** The server's plugin form, for field labels and fixed choices. */
  fields?: readonly PluginAdminFormField[];
}

function overrideValueLabel(key: string, value: unknown, names: OverrideNames): string {
  if (Array.isArray(value)) return value.map((v) => overrideValueLabel(key, v, names)).join(", ");
  if (typeof value === "boolean") return value ? "on" : "off";
  const raw = String(value);
  if (SELF_NAMED_OVERRIDES.has(key)) return raw;
  const choices = names.options?.[key] ?? names.fields?.find((f) => f.key === key)?.options;
  return choices?.find((choice) => choice.value === raw)?.label ?? raw;
}

export function overrideLabel(key: string, names: OverrideNames = {}): string {
  return OVERRIDE_LABELS[key] ?? names.fields?.find((field) => field.key === key)?.label ?? key;
}

function presentOverrides(overrides: Record<string, unknown> | undefined): [string, unknown][] {
  const entries = Object.entries(overrides ?? {}).filter(
    ([, value]) =>
      value !== undefined && value !== null && !(Array.isArray(value) && value.length === 0),
  );
  const rank = (key: string) => {
    const i = INLINE_OVERRIDES.indexOf(key);
    return i === -1 ? INLINE_OVERRIDES.length : i;
  };
  return entries.sort(([a], [b]) => rank(a) - rank(b));
}

/**
 * "Folder /anime · Quality HD-1080p · Tags anime". Values are named from the
 * server's options when they are loaded, and shown as the stored IDs when not.
 */
export function overridesSummary(
  overrides: Record<string, unknown> | undefined,
  names: OverrideNames = {},
): string {
  return presentOverrides(overrides)
    .map(([key, value]) => `${overrideLabel(key, names)} ${overrideValueLabel(key, value, names)}`)
    .join(" · ");
}

/**
 * Where a destination sends a copy, naming only what it overrides:
 * "Sonarr Anime · /tv/anime · Anime 1080p · anime · Series type: Anime".
 */
export function destinationSentence(
  dest: RequestRouteDestination,
  servers: readonly Pick<RequestIntegration, "id" | "name">[],
  names: OverrideNames = {},
): string {
  const server = servers.find((candidate) => candidate.id === dest.integration_id);
  const parts = [server?.name ?? "a server that no longer exists"];
  for (const [key, value] of presentOverrides(dest.overrides)) {
    const label = overrideValueLabel(key, value, names);
    parts.push(INLINE_OVERRIDES.includes(key) ? label : `${overrideLabel(key, names)}: ${label}`);
  }
  return parts.join(" · ");
}

/** Whether two destinations send a copy to the same place with the same settings. */
export function sameDestination(a: RequestRouteDestination, b: RequestRouteDestination): boolean {
  return (
    (a.integration_id ?? "") === (b.integration_id ?? "") &&
    stableJSON(a.overrides ?? {}) === stableJSON(b.overrides ?? {})
  );
}

function stableJSON(value: unknown): string {
  if (Array.isArray(value)) return `[${value.map(stableJSON).join(",")}]`;
  if (value && typeof value === "object") {
    const entries = Object.entries(value as Record<string, unknown>)
      .filter(([, v]) => v !== undefined)
      .sort(([a], [b]) => a.localeCompare(b));
    return `{${entries.map(([k, v]) => `${JSON.stringify(k)}:${stableJSON(v)}`).join(",")}}`;
  }
  return JSON.stringify(value ?? null);
}

export type Tier = "hd" | "uhd";

/** Whether a rule decides a tier for the titles it matches: sends it, or (4K) makes none. */
export function decidesTier(route: Pick<RequestRoute, "hd" | "uhd" | "skip_uhd">, tier: Tier) {
  return tier === "hd"
    ? Boolean(route.hd.integration_id)
    : Boolean(route.uhd.integration_id) || route.skip_uhd;
}

/**
 * What a rule's "pass it on" choice means for a tier: Everything else's
 * destination when no enabled rule below decides that copy, and the rules
 * below otherwise. `below` is every rule after this one.
 */
export function passThroughLabel(
  tier: Tier,
  below: readonly RequestRoute[],
  fallback: Pick<RequestRoute, "hd" | "uhd"> | undefined,
  servers: readonly Pick<RequestIntegration, "id" | "name">[],
): string {
  if (below.some((rule) => rule.enabled && decidesTier(rule, tier))) {
    return "Let the rules below decide";
  }
  const dest = tier === "hd" ? fallback?.hd : fallback?.uhd;
  const server = servers.find((candidate) => candidate.id === dest?.integration_id);
  return `Same as Everything else (${server?.name ?? (tier === "hd" ? "none" : "no 4K version")})`;
}

const OVERRIDABLE_CONTROLS = new Set(["SELECT", "MULTI_SELECT", "SWITCH"]);

/**
 * The plugin fields a route can override on this server: the server's own
 * form minus the keys routing sets and the service kind, and minus fields the
 * form would not show for this server (Radarr's minimum availability on a
 * Sonarr server).
 */
export function overrideFields(
  descriptor: PluginAdminForm | undefined,
  serverConfig: Record<string, unknown>,
): PluginAdminFormField[] {
  if (!descriptor) return [];
  return descriptor.fields.filter(
    (field) =>
      OVERRIDABLE_CONTROLS.has(field.control) &&
      field.key !== SERVICE_KIND_KEY &&
      !ROUTING_OWNED_CONFIG_KEYS.includes(field.key) &&
      evaluateShowWhen(field.show_when, serverConfig, descriptor.fields),
  );
}

/** The fields a route can override on a server, from its plugin's form. */
export function serverOverrideFields(
  server: RequestIntegration,
  installations: RequestRouterInstallation[],
): PluginAdminFormField[] {
  const entry = serverInstallation(installations, server.installation_id, server.capability_id);
  return overrideFields(serverConfigSchema(entry).descriptor, server.plugin_config ?? {});
}

/** A destination's fields: Folder, Quality and Tags on their own rows, the rest under More settings. */
export function splitOverrideFields(fields: readonly PluginAdminFormField[]): {
  inline: PluginAdminFormField[];
  more: PluginAdminFormField[];
} {
  const inline = INLINE_OVERRIDES.flatMap((key) => fields.filter((field) => field.key === key));
  const more = fields.filter((field) => !INLINE_OVERRIDES.includes(field.key));
  return { inline, more };
}

/** Whether a server's form offers Sonarr's Anime series type. */
export function offersAnimeSeriesType(fields: readonly PluginAdminFormField[]): boolean {
  return fields.some(
    (field) =>
      field.key === "series_type" && (field.options ?? []).some((opt) => opt.value === "anime"),
  );
}

/* ------------------------------------------------------------------------ */
/* Content ratings                                                           */
/* ------------------------------------------------------------------------ */

/**
 * Each US rating's own minimum age, as the server's `access.Normalize` gives
 * it. A rule's "rated X or lower" compares these ages, not the parental
 * tiers, so "TV-Y7 or lower" does not take TV-PG and "PG-13 or lower" does
 * not take TV-14. Keyed by the canonical token (letters, digits and "+").
 */
const RATING_AGES: Readonly<Record<string, number>> = {
  G: 0,
  TVY: 0,
  TVG: 0,
  TVY7: 7,
  TVY7FV: 7,
  PG: 8,
  TVPG: 8,
  PG13: 13,
  TV14: 14,
  R: 17,
  TVMA: 17,
  NC17: 18,
};

/** The minimum age a US rating stands for; undefined for one the server would not read. */
export function ratingAge(rating: string | undefined): number | undefined {
  const canon = (rating ?? "")
    .trim()
    .replace(/^rated\s+/i, "")
    .toUpperCase()
    .replace(/[^A-Z0-9+]/g, "");
  return RATING_AGES[canon];
}

/** The US ratings a rule can cap at, youngest first. */
export const ROUTING_RATINGS: readonly string[] = [
  "G",
  "TV-Y",
  "TV-G",
  "TV-Y7",
  "PG",
  "TV-PG",
  "PG-13",
  "TV-14",
  "R",
].sort((a, b) => ratingAge(a)! - ratingAge(b)!);

/* ------------------------------------------------------------------------ */
/* Try a title: facts and the trace                                          */
/* ------------------------------------------------------------------------ */

/**
 * A routing rating in words: "PG-13", or "PG12 (JP)" for a title's own
 * country's rating, which the server prefixes with the country code.
 */
export function ratingLabel(rating: string): string {
  const match = /^([A-Za-z]{2,3}):(.+)$/.exec(rating);
  return match ? `${match[2]} (${match[1]!.toUpperCase()})` : rating;
}

/** "anime · Japanese · Japan · 2001 · Rated PG · Animation, Fantasy". */
export function factsSentence(facts: RequestRouteFacts, mediaType: RequestRouteMediaType): string {
  return [
    facts.anime ? "anime" : null,
    facts.original_language ? routingLanguageName(facts.original_language) : null,
    facts.origin_countries.length > 0
      ? facts.origin_countries.map(routingCountryName).join(", ")
      : null,
    facts.year ? String(facts.year) : null,
    facts.content_rating ? `Rated ${ratingLabel(facts.content_rating)}` : "No rating",
    facts.genre_ids.length > 0 ? genreNames(facts.genre_ids, mediaType).join(", ") : null,
  ]
    .filter(Boolean)
    .join(" · ");
}

export interface TraceContext {
  facts: RequestRouteFacts;
  mediaType: RequestRouteMediaType;
  names: RoutingNames;
  /** The account the preview routed as; undefined for anyone. */
  requesterUserId?: number;
}

function has(values: readonly string[], none: string, label: string) {
  return values.length > 0 ? `${label} ${values.join(", ")}` : none;
}

/**
 * Why a title fails one condition of a rule, from the preview's facts:
 * "genre is Animation (wants Family or Kids)", "no rating (wants PG or lower)".
 * Year bounds are reported once, as `year_from`, for both keys.
 */
function traceMissSentence(
  key: string,
  conditions: RequestRouteConditions,
  ctx: TraceContext,
): string {
  const { facts, mediaType, names } = ctx;
  const c = conditions;
  const genres = genreNames(facts.genre_ids, mediaType);
  const language = facts.original_language ? [routingLanguageName(facts.original_language)] : [];
  const countries = facts.origin_countries.map(routingCountryName);
  const requester =
    ctx.requesterUserId === undefined ? undefined : userNames([ctx.requesterUserId], names)[0];
  switch (key) {
    case "anime":
      return c.anime ? "isn't anime (wants anime)" : "is anime (wants not anime)";
    case "genre_ids":
      return `${has(genres, "no genres", "genre is")} (wants ${orList(genreNames(c.genre_ids ?? [], mediaType))})`;
    case "exclude_genre_ids":
      return `${has(genres, "no genres", "genre is")} (wants anything but ${orList(genreNames(c.exclude_genre_ids ?? [], mediaType))})`;
    case "original_languages":
      return `${has(language, "no original language", "language is")} (wants ${orList((c.original_languages ?? []).map(routingLanguageName))})`;
    case "exclude_original_languages":
      return `${has(language, "no original language", "language is")} (wants anything but ${orList((c.exclude_original_languages ?? []).map(routingLanguageName))})`;
    case "origin_countries":
      return `${has(countries, "no country", "country is")} (wants ${orList((c.origin_countries ?? []).map(routingCountryName))})`;
    case "exclude_origin_countries":
      return `${has(countries, "no country", "country is")} (wants anything but ${orList((c.exclude_origin_countries ?? []).map(routingCountryName))})`;
    case "year_from":
    case "year_to":
      return `${facts.year ? `year is ${facts.year}` : "no year"} (wants ${yearRangeLabel(c.year_from, c.year_to)})`;
    case "max_content_rating":
      return `${facts.content_rating ? `rated ${ratingLabel(facts.content_rating)}` : "no rating"} (wants ${c.max_content_rating} or lower)`;
    case "network_ids":
    case "exclude_network_ids": {
      const on = has(brandNames(facts.network_ids, "network", names), "no network", "network is");
      const wanted = brandNames(c[key] ?? [], "network", names);
      return `${on} (wants ${key === "network_ids" ? "" : "anything but "}${orList(wanted)})`;
    }
    case "company_ids":
    case "exclude_company_ids": {
      const by = has(brandNames(facts.company_ids, "studio", names), "no studio", "studio is");
      const wanted = brandNames(c[key] ?? [], "studio", names);
      return `${by} (wants ${key === "company_ids" ? "" : "anything but "}${orList(wanted)})`;
    }
    case "requester_user_ids":
      return `requested by ${requester ?? "anyone"} (wants ${orList(userNames(c.requester_user_ids ?? [], names))})`;
    case "exclude_requester_user_ids":
      return `requested by ${requester ?? "anyone"} (wants anyone but ${orList(userNames(c.exclude_requester_user_ids ?? [], names))})`;
    case "keyword_ids":
      return `no matching TMDB keyword (wants ${orList((c.keyword_ids ?? []).map(String))})`;
    case "exclude_keyword_ids": {
      const excluded = c.exclude_keyword_ids ?? [];
      const found = excluded.filter((id) => facts.keyword_ids.includes(id)).map(String);
      return `has TMDB keyword ${orList(found)} (wants none of ${orList(excluded.map(String))})`;
    }
    default:
      return `fails ${key}`;
  }
}

/** Every miss of a rule, year bounds once. */
function traceMisses(
  unmet: readonly string[],
  conditions: RequestRouteConditions,
  ctx: TraceContext,
): string[] {
  const keys = unmet.filter(
    (key, i) => key !== "year_to" || !unmet.slice(0, i).includes("year_from"),
  );
  return keys.map((key) => traceMissSentence(key, conditions, ctx));
}

/** What a matching rule did: "decides HD", "decides HD and 4K", "decides HD: none", "decides 4K: none". */
function traceDecision(rule: Pick<RequestRoutePreviewRule, "hd" | "uhd">): string {
  const parts: string[] = [];
  if (rule.hd === "sends" && rule.uhd === "sends") return "decides HD and 4K";
  if (rule.hd === "sends") parts.push("decides HD");
  if (rule.hd === "skips") parts.push("decides HD: none");
  if (rule.uhd === "sends") parts.push("decides 4K");
  if (rule.uhd === "skips") parts.push("decides 4K: none");
  if (parts.length > 0) return parts.join(" · ");
  const passed = rule.hd === "passes" || rule.uhd === "passes";
  return passed ? "leaves it to the rules below" : "earlier rules already decided";
}

/**
 * One line of "How it was decided" for a rule of the preview:
 * "1. Anime — matches · decides HD", "Off: Old anime",
 * "3. Kids & family — doesn't match: genre is Animation (wants Family or Kids)".
 */
export function traceLine(
  rule: RequestRoutePreviewRule,
  number: number,
  conditions: RequestRouteConditions | undefined,
  ctx: TraceContext,
): string {
  if (rule.is_fallback) return `Everything else — ${traceDecision(rule)}`;
  if (!rule.enabled) return `Off: ${rule.route_name}`;
  const head = `${number}. ${rule.route_name}`;
  if (rule.unmet_conditions.length === 0) return `${head} — matches · ${traceDecision(rule)}`;
  const misses = conditions
    ? traceMisses(rule.unmet_conditions, conditions, ctx)
    : rule.unmet_conditions;
  return `${head} — doesn't match: ${misses.join("; ")}`;
}
