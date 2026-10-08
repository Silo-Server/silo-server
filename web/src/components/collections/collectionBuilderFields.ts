import type { PersonalizedSorts } from "@/lib/querySortOptions";
import type { FilterRule, QuerySort } from "@/api/types";
import { getQuerySortOptions, type QuerySortRelevanceScope } from "@/lib/querySortOptions";

export interface CollectionOperatorOption {
  value: string;
  label: string;
  /** Kept for saved rules that use it, but not offered for a new one. */
  hidden?: boolean;
}

/** Where a field sits in the field picker. */
export type CollectionFieldGroup = "title" | "people" | "file" | "you" | "library";

export const COLLECTION_FIELD_GROUPS: ReadonlyArray<[CollectionFieldGroup, string]> = [
  ["title", "Title"],
  ["people", "People"],
  ["file", "File"],
  ["you", "You"],
  ["library", "Library"],
];

/** The catalog facets a rule value can be picked from. */
export type CollectionValueFacet = "genre" | "studio" | "network" | "country" | "content_rating";

export interface CollectionFieldOption {
  value: string;
  label: string;
  group: CollectionFieldGroup;
  operators: CollectionOperatorOption[];
  /**
   * `facet` picks from the values titles carry; `date` is a calendar date, or
   * "in the last" a number of days, weeks, months or years.
   */
  inputType: "text" | "number" | "select" | "boolean" | "person_search" | "facet" | "date";
  valueType?: "string" | "number" | "boolean";
  supportsRange?: boolean;
  selectOptions?: Array<{ value: string; label: string }>;
  personalized?: boolean;
  /** Kept for saved rules that use it, but not offered for a new one. */
  hidden?: boolean;
  /** For `facet` fields: the facet to search, the picker's empty label and its search box's name. */
  facet?: CollectionValueFacet;
  placeholder?: string;
  searchLabel?: string;
  /** Shown after a number, in the unit the server compares. */
  unit?: string;
}

const IS_OPERATORS: CollectionOperatorOption[] = [
  { value: "is", label: "is" },
  { value: "is_not", label: "is not" },
];

const NUMBER_OPERATORS: CollectionOperatorOption[] = [
  { value: "gte", label: "is at least" },
  { value: "lte", label: "is at most" },
  { value: "gt", label: "is above" },
  { value: "lt", label: "is below" },
  { value: "between", label: "is between" },
];

const DATE_OPERATORS: CollectionOperatorOption[] = [
  { value: "gt", label: "after" },
  { value: "lt", label: "before" },
  { value: "between", label: "between" },
  { value: "in_last", label: "in the last" },
];

const SWITCH_OPERATORS: CollectionOperatorOption[] = [{ value: "is", label: "is" }];

function facetField(
  value: CollectionValueFacet,
  label: string,
  [noun, plural]: [string, string],
  operators = IS_OPERATORS,
): CollectionFieldOption {
  return {
    value,
    label,
    group: "title",
    operators,
    inputType: "facet",
    valueType: "string",
    facet: value,
    placeholder: `Pick a ${noun}`,
    searchLabel: `Search ${plural}`,
  };
}

function personField(value: string, label: string): CollectionFieldOption {
  return {
    value,
    label,
    group: "people",
    operators: IS_OPERATORS,
    inputType: "person_search",
    valueType: "string",
  };
}

function switchField(
  value: string,
  label: string,
  group: CollectionFieldGroup,
  personalized = false,
): CollectionFieldOption {
  return {
    value,
    label,
    group,
    operators: SWITCH_OPERATORS,
    inputType: "boolean",
    valueType: "boolean",
    ...(personalized ? { personalized } : {}),
  };
}

/** In field-picker order: grouped Title, People, File, You, Library. */
export const COLLECTION_FIELD_OPTIONS: CollectionFieldOption[] = [
  facetField(
    "genre",
    "Genre",
    ["genre", "genres"],
    [...IS_OPERATORS, { value: "contains", label: "contains", hidden: true }],
  ),
  facetField("studio", "Studio", ["studio", "studios"]),
  facetField("network", "Network", ["network", "networks"]),
  facetField("country", "Country", ["country", "countries"]),
  facetField("content_rating", "Content rating", ["rating", "content ratings"]),
  {
    value: "type",
    label: "Type",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: [
      { value: "movie", label: "Movies" },
      { value: "series", label: "Shows" },
    ],
  },
  {
    value: "year",
    label: "Year",
    group: "title",
    operators: [{ value: "is", label: "is" }, ...NUMBER_OPERATORS],
    inputType: "number",
    valueType: "number",
    supportsRange: true,
  },
  {
    value: "release_date",
    label: "Release date",
    group: "title",
    operators: DATE_OPERATORS,
    inputType: "date",
    valueType: "string",
    supportsRange: true,
  },
  {
    value: "rating_imdb",
    label: "IMDb rating",
    group: "title",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
  },
  {
    // The metadata match state, which only explains a saved rule.
    value: "status",
    label: "Status",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: [
      { value: "pending", label: "pending" },
      { value: "matched", label: "matched" },
      { value: "unmatched", label: "unmatched" },
    ],
    hidden: true,
  },
  personField("actor", "Actor"),
  personField("director", "Director"),
  personField("writer", "Writer"),
  personField("producer", "Producer"),
  {
    value: "resolution",
    label: "Resolution",
    group: "file",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: ["480p", "720p", "1080p", "2160p", "4320p"].map((value) => ({
      value,
      label: value,
    })),
  },
  switchField("hdr", "HDR", "file"),
  switchField("dolby_vision", "Dolby Vision", "file"),
  {
    // media_files.bitrate holds kilobits per second.
    value: "bitrate",
    label: "Bitrate",
    group: "file",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    unit: "kbps",
  },
  switchField("watched", "Watched", "you", true),
  switchField("favorited", "Favorited", "you", true),
  switchField("in_watchlist", "In watchlist", "you", true),
  switchField("in_progress", "In progress", "you", true),
  {
    value: "added_at",
    label: "Added",
    group: "library",
    operators: DATE_OPERATORS,
    inputType: "date",
    valueType: "string",
    supportsRange: true,
  },
];

export function getCollectionSortOptions(
  includePersonalized: PersonalizedSorts = false,
  relevanceScope?: QuerySortRelevanceScope,
  shownRatingSources?: ReadonlySet<string>,
  keepSortField?: string,
): Array<{ value: QuerySort["field"]; label: string }> {
  return getQuerySortOptions({
    includePersonalized,
    relevanceScope,
    shownRatingSources,
    keepSortField,
  }).map((option) => ({
    value: option.value,
    label: option.label,
  }));
}

export function getCollectionFieldOption(field: string): CollectionFieldOption | undefined {
  const normalizedField = field === "rating" ? "rating_imdb" : field;
  return COLLECTION_FIELD_OPTIONS.find((option) => option.value === normalizedField);
}

/** The value a rule starts with when its field or condition changes. */
export function getDefaultRuleValue(field: string, op: string): FilterRule["value"] {
  const fieldDef = getCollectionFieldOption(field);
  if (op === "between" && fieldDef?.supportsRange) {
    return ["", ""];
  }
  if (fieldDef?.inputType === "boolean") {
    return false;
  }
  if (fieldDef?.inputType === "number") {
    return 0;
  }
  return "";
}

/** The rule a new line starts with. */
export function newFilterRule(): FilterRule {
  return { field: "genre", op: "is", value: getDefaultRuleValue("genre", "is") };
}
