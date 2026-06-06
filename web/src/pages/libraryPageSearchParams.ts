import {
  createEmptyQueryDefinition,
  normalizeQueryDefinition,
  type QueryDefinition,
  type QueryGroup,
  type QueryRule,
} from "@/api/types";
import {
  normalizeQuerySortField,
  normalizeQuerySortForScope,
  type QuerySortRelevanceScope,
} from "@/lib/querySortOptions";

export type LibraryPageTab = "recommended" | "library" | "collections";
export type LibraryBrowseType = "series" | "episode";

export interface LibraryPageState {
  activeTab: LibraryPageTab;
  browseType: LibraryBrowseType;
  queryDefinition: QueryDefinition;
}

const GROUP_MATCH_PATTERN = /^groups\[(\d+)\]\[match\]$/;
const GROUP_RULE_PATTERN = /^groups\[(\d+)\]\[rules\]\[(\d+)\]\[(field|op|value)\]$/;
const GROUP_RULE_VALUE_PATTERN = /^groups\[(\d+)\]\[rules\]\[(\d+)\]\[value\]\[(\d+)\]$/;

const LIBRARY_QUERY_KEYS = new Set([
  "tab",
  "type",
  "sort",
  "order",
  "match",
  "genre",
  "year_min",
  "year_max",
  "content_rating",
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

function createDefaultLibraryQueryDefinition(): QueryDefinition {
  return normalizeQueryDefinition({
    ...createEmptyQueryDefinition(),
    sort: { field: "title", order: "asc" },
  });
}

function parseSeriesLibraryBrowseType(value: string | undefined): LibraryBrowseType {
  return value === "episode" ? "episode" : "series";
}

function getLibrarySortRelevanceScope(
  libraryType: string,
  mediaScope?: QueryDefinition["media_scope"],
): QuerySortRelevanceScope {
  if (libraryType === "movie" || libraryType === "series") {
    return libraryType;
  }
  // The DB stores audiobook library type as the plural "audiobooks";
  // the sort scope is the singular "audiobook" (matches QueryDefinition.media_scope).
  if (libraryType === "audiobook" || libraryType === "audiobooks") {
    return "audiobook";
  }
  if (
    mediaScope === "movie" ||
    mediaScope === "series" ||
    mediaScope === "episode" ||
    mediaScope === "audiobook"
  ) {
    return mediaScope;
  }
  return "all";
}

function readString(value: string | null): string | undefined {
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

function parseGroups(searchParams: URLSearchParams): QueryGroup[] {
  const groups = new Map<number, GroupBuilder>();

  searchParams.forEach((rawValue, key) => {
    const groupMatch = key.match(GROUP_MATCH_PATTERN);
    if (groupMatch) {
      const groupIndex = Number(groupMatch[1]);
      ensureGroup(groups, groupIndex).match = rawValue === "any" ? "any" : "all";
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

function hasRuleForField(groups: QueryGroup[], field: string): boolean {
  return groups.some((group) => group.rules.some((rule) => rule.field === field));
}

function buildLegacyImplicitGroups(
  searchParams: URLSearchParams,
  groups: QueryGroup[],
): QueryGroup[] {
  const implicitGroups: QueryGroup[] = [];
  const genre = readString(searchParams.get("genre"));
  const yearMin = parsePositiveInt(searchParams.get("year_min"));
  const yearMax = parsePositiveInt(searchParams.get("year_max"));
  const contentRating = readString(searchParams.get("content_rating"));

  if (genre && !hasRuleForField(groups, "genre")) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "genre", op: "contains", value: genre }],
    });
  }

  if (!hasRuleForField(groups, "year")) {
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
  }

  if (contentRating && !hasRuleForField(groups, "content_rating")) {
    implicitGroups.push({
      match: "all",
      rules: [{ field: "content_rating", op: "is", value: contentRating }],
    });
  }

  return implicitGroups;
}

export function parseLibraryPageState(
  searchParams: URLSearchParams,
  libraryType: string,
): LibraryPageState {
  const activeTab: LibraryPageTab =
    searchParams.get("tab") === "library"
      ? "library"
      : searchParams.get("tab") === "collections"
        ? "collections"
        : "recommended";

  const browseType =
    libraryType === "series"
      ? parseSeriesLibraryBrowseType(readString(searchParams.get("type")))
      : "series";
  const defaultQueryDefinition = createDefaultLibraryQueryDefinition();
  if (activeTab !== "library") {
    return {
      activeTab,
      browseType,
      queryDefinition: defaultQueryDefinition,
    };
  }

  const parsedGroups = parseGroups(searchParams);
  const implicitGroups = buildLegacyImplicitGroups(searchParams, parsedGroups);
  const mediaScopeParam = readString(searchParams.get("type"));
  const mediaScope =
    libraryType === "mixed" &&
    (mediaScopeParam === "movie" || mediaScopeParam === "series" || mediaScopeParam === "episode")
      ? mediaScopeParam
      : undefined;
  const sortRelevanceScope =
    libraryType === "series" && browseType === "episode"
      ? "all"
      : getLibrarySortRelevanceScope(libraryType, mediaScope);
  const sortField = normalizeQuerySortField(readString(searchParams.get("sort")));
  const orderParam = readString(searchParams.get("order"));
  const normalizedSort = normalizeQuerySortForScope(
    {
      field: sortField ?? defaultQueryDefinition.sort.field,
      order: orderParam === "asc" || orderParam === "desc" ? orderParam : undefined,
    },
    { includePersonalized: true, relevanceScope: sortRelevanceScope },
  );

  return {
    activeTab,
    browseType,
    queryDefinition: normalizeQueryDefinition({
      ...defaultQueryDefinition,
      media_scope: mediaScope,
      match: searchParams.get("match") === "any" ? "any" : "all",
      groups: [...implicitGroups, ...parsedGroups],
      sort: normalizedSort,
    }),
  };
}

export function hasLibraryPageSearchParams(searchParams: URLSearchParams): boolean {
  for (const key of searchParams.keys()) {
    if (
      LIBRARY_QUERY_KEYS.has(key) ||
      GROUP_MATCH_PATTERN.test(key) ||
      GROUP_RULE_PATTERN.test(key) ||
      GROUP_RULE_VALUE_PATTERN.test(key)
    ) {
      return true;
    }
  }
  return false;
}

function deleteLibraryQueryParams(nextSearchParams: URLSearchParams) {
  for (const key of Array.from(nextSearchParams.keys())) {
    if (
      LIBRARY_QUERY_KEYS.has(key) ||
      GROUP_MATCH_PATTERN.test(key) ||
      GROUP_RULE_PATTERN.test(key) ||
      GROUP_RULE_VALUE_PATTERN.test(key)
    ) {
      nextSearchParams.delete(key);
    }
  }
}

export function serializeLibraryPageSearchParams(searchParams: URLSearchParams): string {
  const librarySearchParams = new URLSearchParams(searchParams);
  for (const key of Array.from(librarySearchParams.keys())) {
    if (
      !LIBRARY_QUERY_KEYS.has(key) &&
      !GROUP_MATCH_PATTERN.test(key) &&
      !GROUP_RULE_PATTERN.test(key) &&
      !GROUP_RULE_VALUE_PATTERN.test(key)
    ) {
      librarySearchParams.delete(key);
    }
  }
  return librarySearchParams.toString();
}

export function applySavedLibraryPageSearchParams(
  currentSearchParams: URLSearchParams,
  savedSearch: string,
): URLSearchParams {
  const nextSearchParams = new URLSearchParams(currentSearchParams);
  deleteLibraryQueryParams(nextSearchParams);
  const savedSearchParams = new URLSearchParams(savedSearch);
  savedSearchParams.forEach((value, key) => {
    nextSearchParams.append(key, value);
  });
  return nextSearchParams;
}

export function updateLibraryPageSearchParams(
  currentSearchParams: URLSearchParams,
  state: LibraryPageState,
  libraryType: string,
): URLSearchParams {
  const nextSearchParams = new URLSearchParams(currentSearchParams);
  deleteLibraryQueryParams(nextSearchParams);

  if (state.activeTab !== "library") {
    if (state.activeTab === "collections") {
      nextSearchParams.set("tab", "collections");
    }
    return nextSearchParams;
  }

  const defaultQueryDefinition = createDefaultLibraryQueryDefinition();
  const sortRelevanceScope =
    libraryType === "series" && state.browseType === "episode"
      ? "all"
      : getLibrarySortRelevanceScope(libraryType, state.queryDefinition.media_scope);
  const queryDefinition = normalizeQueryDefinition({
    ...state.queryDefinition,
    sort: normalizeQuerySortForScope(state.queryDefinition.sort, {
      includePersonalized: true,
      relevanceScope: sortRelevanceScope,
    }),
  });

  nextSearchParams.set("tab", "library");

  if (libraryType === "series") {
    if (state.browseType === "episode") {
      nextSearchParams.set("type", "episode");
    }
  } else if (libraryType === "mixed" && queryDefinition.media_scope) {
    nextSearchParams.set("type", queryDefinition.media_scope);
  }

  if (queryDefinition.match !== "all") {
    nextSearchParams.set("match", queryDefinition.match);
  }

  if (queryDefinition.sort.field !== defaultQueryDefinition.sort.field) {
    nextSearchParams.set("sort", queryDefinition.sort.field);
  }
  if (
    queryDefinition.sort.order &&
    (queryDefinition.sort.field !== defaultQueryDefinition.sort.field ||
      queryDefinition.sort.order !== defaultQueryDefinition.sort.order)
  ) {
    nextSearchParams.set("order", queryDefinition.sort.order);
  }

  queryDefinition.groups.forEach((group, groupIndex) => {
    nextSearchParams.set(`groups[${groupIndex}][match]`, group.match);
    group.rules.forEach((rule, ruleIndex) => {
      nextSearchParams.set(`groups[${groupIndex}][rules][${ruleIndex}][field]`, rule.field);
      nextSearchParams.set(`groups[${groupIndex}][rules][${ruleIndex}][op]`, rule.op);
      if (Array.isArray(rule.value)) {
        rule.value.forEach((entry, valueIndex) => {
          nextSearchParams.set(
            `groups[${groupIndex}][rules][${ruleIndex}][value][${valueIndex}]`,
            String(entry),
          );
        });
      } else {
        nextSearchParams.set(
          `groups[${groupIndex}][rules][${ruleIndex}][value]`,
          String(rule.value),
        );
      }
    });
  });

  return nextSearchParams;
}
