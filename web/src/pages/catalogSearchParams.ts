import {
  createEmptyQueryDefinition,
  normalizeQueryDefinition,
  type CatalogSource,
  type QueryDefinition,
  type QueryGroup,
  type QueryRule,
} from "@/api/types";
import { normalizeQuerySortField } from "@/lib/querySortOptions";

const GROUP_MATCH_PATTERN = /^groups\[(\d+)\]\[match\]$/;
const GROUP_RULE_PATTERN = /^groups\[(\d+)\]\[rules\]\[(\d+)\]\[(field|op|value)\]$/;
const GROUP_RULE_VALUE_PATTERN = /^groups\[(\d+)\]\[rules\]\[(\d+)\]\[value\]\[(\d+)\]$/;

export interface CatalogSearchState {
  source: CatalogSource;
  title?: string;
  q?: string;
  scope?: "home" | "library";
  section_id?: string;
  library_id?: number;
  collection_id?: string;
  person_id?: string;
  type_override?: string;
  query_definition: QueryDefinition;
}

export interface SectionCatalogDestination {
  scope: "home" | "library";
  sectionId: string;
  libraryId?: number;
  title?: string;
}

const SECTION_BROWSE_SUPPORT_TYPES = new Set([
  "collection",
  "custom_filter",
  "genre",
  "random",
  "recently_added",
]);

interface GroupBuilder {
  match?: "all" | "any";
  rules: Map<number, RuleBuilder>;
}

interface RuleBuilder {
  field?: string;
  op?: string;
  value?: unknown;
  values?: Map<number, unknown>;
}

const overlaySources = new Set<CatalogSource>([
  "query",
  "favorites",
  "watchlist",
  "history",
  "person",
]);

export function catalogSourceAllowsOverlay(source: CatalogSource): boolean {
  return overlaySources.has(source);
}

export function parseCatalogSearchParams(searchParams: URLSearchParams): CatalogSearchState {
  const source = parseCatalogSource(searchParams.get("source"));
  const title = readString(searchParams.get("title"));

  const baseState: CatalogSearchState = {
    source,
    title,
    q: readString(searchParams.get("q")),
    scope: undefined,
    section_id: undefined,
    library_id: parsePositiveInt(searchParams.get("library_id")) ?? undefined,
    collection_id: readString(searchParams.get("collection_id")),
    person_id: readPersonId(searchParams.get("person_id")),
    query_definition: createEmptyQueryDefinition(),
  };

  if (source === "section") {
    baseState.scope = searchParams.get("scope") === "home" ? "home" : "library";
    baseState.section_id = readString(searchParams.get("section_id"));
  }

  if (!catalogSourceAllowsOverlay(source)) {
    return baseState;
  }

  const groups = parseCatalogGroups(searchParams);
  const implicitGroups: QueryGroup[] = [];
  const type = readString(searchParams.get("type"));
  const genre = readString(searchParams.get("genre"));
  const yearMin = parsePositiveInt(searchParams.get("year_min"));
  const yearMax = parsePositiveInt(searchParams.get("year_max"));
  const contentRating = readString(searchParams.get("content_rating"));

  if (genre) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: genre }],
    });
  }

  if (yearMin != null && yearMax != null) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "year", op: "between", value: [yearMin, yearMax] }],
    });
  } else if (yearMin != null) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "year", op: "gte", value: yearMin }],
    });
  } else if (yearMax != null) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "year", op: "lte", value: yearMax }],
    });
  }

  if (contentRating) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "content_rating", op: "is", value: contentRating }],
    });
  }

  const sort = normalizeQuerySortField(readString(searchParams.get("sort")));
  const rawOrder = readString(searchParams.get("order"));
  const queryLimit = parsePositiveInt(searchParams.get("query_limit")) ?? undefined;

  baseState.query_definition = normalizeQueryDefinition({
    library_ids: baseState.library_id ? [baseState.library_id] : [],
    media_scope:
      type === "movie" ||
      type === "series" ||
      type === "episode" ||
      type === "audiobook" ||
      type === "ebook"
        ? type
        : undefined,
    match: searchParams.get("match") === "any" ? "any" : "all",
    groups: [...implicitGroups, ...groups],
    sort: sort
      ? {
          field: sort,
          order: rawOrder === "asc" || rawOrder === "desc" ? rawOrder : undefined,
        }
      : undefined,
    limit: queryLimit,
  });

  return baseState;
}

export function buildCatalogHref(state: CatalogSearchState): string {
  const params = buildCatalogApiSearchParams(state);
  return `/catalog?${params.toString()}`;
}

export function buildQueryCatalogHref(q?: string): string {
  return buildCatalogHref({
    source: "query",
    q,
    query_definition: createEmptyQueryDefinition(),
  });
}

export function buildPersonalCatalogHref(source: "favorites" | "watchlist" | "history"): string {
  return buildCatalogHref({
    source,
    query_definition: createEmptyQueryDefinition(),
  });
}

export function buildPersonCatalogHref(personId: string): string {
  return `/person/${personId}`;
}

export function buildSectionCatalogHref(destination: SectionCatalogDestination): string {
  return buildCatalogHref({
    source: "section",
    scope: destination.scope,
    section_id: destination.sectionId,
    library_id: destination.scope === "library" ? destination.libraryId : undefined,
    title: destination.title,
    query_definition: createEmptyQueryDefinition(),
  });
}

export function buildLibraryCollectionCatalogHref(collectionId: string, title?: string): string {
  return buildCatalogHref({
    source: "library_collection",
    collection_id: collectionId,
    title,
    query_definition: createEmptyQueryDefinition(),
  });
}

export function buildUserCollectionCatalogHref(collectionId: string, title?: string): string {
  return buildCatalogHref({
    source: "user_collection",
    collection_id: collectionId,
    title,
    query_definition: createEmptyQueryDefinition(),
  });
}

export function buildLegacyBrowseCatalogHref(searchParams: URLSearchParams): string | null {
  const source = searchParams.get("source");

  if (source === "collection") {
    const collectionId = readString(searchParams.get("collection_id"));
    if (!collectionId) {
      return null;
    }

    return buildLibraryCollectionCatalogHref(collectionId, readString(searchParams.get("title")));
  }

  if (source !== "section") {
    return null;
  }

  const sectionId = readString(searchParams.get("section_id"));
  if (!sectionId) {
    return null;
  }

  const scope = searchParams.get("scope") === "home" ? "home" : "library";
  if (scope === "library") {
    const libraryId = parsePositiveInt(searchParams.get("library_id"));
    if (!libraryId) {
      return null;
    }

    return buildSectionCatalogHref({
      scope,
      sectionId,
      libraryId,
      title: readString(searchParams.get("title")),
    });
  }

  return buildSectionCatalogHref({
    scope,
    sectionId,
    title: readString(searchParams.get("title")),
  });
}

export function isSectionBrowseSupported(sectionType: string): boolean {
  return SECTION_BROWSE_SUPPORT_TYPES.has(sectionType);
}

export function buildCatalogApiSearchParams(state: CatalogSearchState): URLSearchParams {
  const params = new URLSearchParams();
  params.set("source", state.source);

  if (state.source === "section") {
    if (state.scope) {
      params.set("scope", state.scope);
    }
    if (state.section_id) {
      params.set("section_id", state.section_id);
    }
    if (state.scope === "library" && state.library_id) {
      params.set("library_id", String(state.library_id));
    }
    if (state.title) {
      params.set("title", state.title);
    }
    return params;
  }

  if (state.source === "library_collection" || state.source === "user_collection") {
    if (state.collection_id) {
      params.set("collection_id", state.collection_id);
    }
    if (state.title) {
      params.set("title", state.title);
    }
    return params;
  }

  if (state.collection_id) {
    params.set("collection_id", state.collection_id);
  }
  if (state.source === "person" && state.person_id) {
    params.set("person_id", String(state.person_id));
  }
  if (state.title) {
    params.set("title", state.title);
  }
  if (state.q) {
    params.set("q", state.q);
  }
  if (state.type_override) {
    params.set("type", state.type_override);
  } else if (state.query_definition.media_scope) {
    params.set("type", state.query_definition.media_scope);
  }
  const effectiveLibraryID = state.library_id ?? state.query_definition.library_ids[0];
  if (state.library_id) {
    params.set("library_id", String(state.library_id));
  } else if (effectiveLibraryID) {
    params.set("library_id", String(effectiveLibraryID));
  }

  if (state.query_definition.sort.field === "relevance") {
    params.set("sort", "relevance");
    params.set("order", state.query_definition.sort.order);
  } else if (
    state.query_definition.sort.field &&
    (state.query_definition.sort.field !== "added_at" ||
      (state.source === "query" && effectiveLibraryID != null))
  ) {
    params.set("sort", state.query_definition.sort.field);
    if (state.query_definition.sort.order) {
      params.set("order", state.query_definition.sort.order);
    }
  }
  if (state.query_definition.limit != null && state.query_definition.limit > 0) {
    params.set("query_limit", String(state.query_definition.limit));
  }

  state.query_definition.groups.forEach((group, groupIndex) => {
    params.set(`groups[${groupIndex}][match]`, group.match);
    group.rules.forEach((rule, ruleIndex) => {
      params.set(`groups[${groupIndex}][rules][${ruleIndex}][field]`, rule.field);
      params.set(`groups[${groupIndex}][rules][${ruleIndex}][op]`, rule.op);
      if (Array.isArray(rule.value)) {
        rule.value.forEach((entry, valueIndex) => {
          params.set(
            `groups[${groupIndex}][rules][${ruleIndex}][value][${valueIndex}]`,
            String(entry),
          );
        });
      } else {
        params.set(`groups[${groupIndex}][rules][${ruleIndex}][value]`, String(rule.value));
      }
    });
  });

  return params;
}

function parseCatalogSource(value: string | null): CatalogSource {
  switch (value) {
    case "section":
    case "library_collection":
    case "user_collection":
    case "favorites":
    case "watchlist":
    case "history":
    case "person":
      return value;
    default:
      return "query";
  }
}

function readString(value: string | null): string | undefined {
  const normalized = value?.trim();
  return normalized ? normalized : undefined;
}

function readPersonId(value: string | null): string | undefined {
  const normalized = value?.trim();
  return normalized ? normalized : undefined;
}

function parsePositiveInt(value: string | null): number | null {
  if (!value) {
    return null;
  }
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : null;
}

function parseCatalogGroups(searchParams: URLSearchParams): QueryGroup[] {
  const groups = new Map<number, GroupBuilder>();

  searchParams.forEach((rawValue, key) => {
    const groupMatch = key.match(GROUP_MATCH_PATTERN);
    if (groupMatch) {
      const groupIndex = Number(groupMatch[1]);
      const group = ensureGroup(groups, groupIndex);
      group.match = rawValue === "any" ? "any" : "all";
      return;
    }

    const ruleMatch = key.match(GROUP_RULE_PATTERN);
    if (ruleMatch) {
      const groupIndex = Number(ruleMatch[1]);
      const ruleIndex = Number(ruleMatch[2]);
      const fieldName = ruleMatch[3];
      const rule = ensureRule(groups, groupIndex, ruleIndex);
      if (fieldName === "field") {
        rule.field = rawValue;
      } else if (fieldName === "op") {
        rule.op = rawValue;
      } else {
        rule.value = parseScalar(rawValue);
      }
      return;
    }

    const indexedValueMatch = key.match(GROUP_RULE_VALUE_PATTERN);
    if (indexedValueMatch) {
      const groupIndex = Number(indexedValueMatch[1]);
      const ruleIndex = Number(indexedValueMatch[2]);
      const valueIndex = Number(indexedValueMatch[3]);
      const rule = ensureRule(groups, groupIndex, ruleIndex);
      rule.values ??= new Map<number, unknown>();
      rule.values.set(valueIndex, parseScalar(rawValue));
    }
  });

  return Array.from(groups.entries())
    .sort(([left], [right]) => left - right)
    .map(([, group]) => ({
      match: group.match ?? "all",
      rules: Array.from(group.rules.entries())
        .sort(([left], [right]) => left - right)
        .map(([, rule]) => ({
          field: rule.field ?? "genre",
          op: rule.op ?? "is",
          value:
            rule.values && rule.values.size > 0
              ? (Array.from(rule.values.entries())
                  .sort(([left], [right]) => left - right)
                  .map(([, value]) => value) as QueryRule["value"])
              : ((rule.value ?? "") as QueryRule["value"]),
        })),
    }))
    .filter((group) => group.rules.length > 0);
}

function ensureGroup(groups: Map<number, GroupBuilder>, index: number): GroupBuilder {
  let group = groups.get(index);
  if (!group) {
    group = { rules: new Map<number, RuleBuilder>() };
    groups.set(index, group);
  }
  return group;
}

function ensureRule(
  groups: Map<number, GroupBuilder>,
  groupIndex: number,
  ruleIndex: number,
): RuleBuilder {
  const group = ensureGroup(groups, groupIndex);
  let rule = group.rules.get(ruleIndex);
  if (!rule) {
    rule = {};
    group.rules.set(ruleIndex, rule);
  }
  return rule;
}

function parseScalar(value: string): string | number | boolean {
  if (value === "true") {
    return true;
  }
  if (value === "false") {
    return false;
  }
  if (/^-?\d+$/.test(value)) {
    return Number(value);
  }
  return value;
}
