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

/** Which of a title's languages a language rule compares. */
export type CollectionLanguageSource = "original" | "audio" | "subtitle";

export interface CollectionFieldOption {
  value: string;
  label: string;
  group: CollectionFieldGroup;
  operators: CollectionOperatorOption[];
  /**
   * `facet` picks from the values titles carry; `date` is a calendar date, or
   * "in the last" a number of days, weeks, months or years; `language` picks
   * from the languages titles have.
   */
  inputType:
    | "text"
    | "number"
    | "select"
    | "boolean"
    | "person_search"
    | "facet"
    | "date"
    | "language";
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
  /** For `language` fields: which of a title's languages the rule compares. */
  languageSource?: CollectionLanguageSource;
  /**
   * The rating source the field compares, for a rating an administrator can
   * hide. Offered only while that source is shown.
   */
  ratingSource?: string;
  /** Describes a show's episodes, so it is offered only where shows can match. */
  showsOnly?: boolean;
  /** Taken only by a server that advertises `extended_query_rules`. */
  extended?: boolean;
  /** Episodes carry no value for it, so it is not offered where only episodes match. */
  noEpisodeValue?: boolean;
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

/** Whole or part of a free-text value, compared ignoring case. */
const TEXT_OPERATORS: CollectionOperatorOption[] = [
  { value: "contains", label: "contains" },
  { value: "not_contains", label: "does not contain" },
  { value: "is", label: "is" },
  { value: "is_not", label: "is not" },
  { value: "begins_with", label: "begins with" },
  { value: "ends_with", label: "ends with" },
];

/** A span ending today first, since rows and collections mostly want recent titles. */
const DATE_OPERATORS: CollectionOperatorOption[] = [
  { value: "in_last", label: "is in the last" },
  { value: "not_in_last", label: "is not in the last" },
  { value: "lt", label: "is before" },
  { value: "gt", label: "is after" },
  { value: "between", label: "is between" },
];

/** The conditions that take a span such as "30d" rather than a date. */
export const RELATIVE_DATE_OPERATORS: ReadonlySet<string> = new Set(["in_last", "not_in_last"]);

/** Decades from this one back to the 1900s, as the decade's first year. */
function decadeOptions(): Array<{ value: string; label: string }> {
  const latest = Math.floor(new Date().getFullYear() / 10) * 10;
  const options = [];
  for (let decade = latest; decade >= 1900; decade -= 10) {
    options.push({ value: String(decade), label: `${decade}s` });
  }
  return options;
}

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

function dateField(
  value: string,
  label: string,
  group: CollectionFieldGroup,
): CollectionFieldOption {
  return {
    value,
    label,
    group,
    operators: DATE_OPERATORS,
    inputType: "date",
    valueType: "string",
    supportsRange: true,
  };
}

function ratingField(value: string, label: string, ratingSource?: string): CollectionFieldOption {
  return {
    value,
    label,
    group: "title",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    ...(ratingSource ? { ratingSource } : {}),
  };
}

function languageField(
  value: string,
  label: string,
  group: CollectionFieldGroup,
  languageSource: CollectionLanguageSource,
): CollectionFieldOption {
  return {
    value,
    label,
    group,
    operators: IS_OPERATORS,
    inputType: "language",
    valueType: "string",
    languageSource,
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
  {
    value: "title",
    label: "Title",
    group: "title",
    operators: TEXT_OPERATORS,
    inputType: "text",
    valueType: "string",
    extended: true,
  },
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
  languageField("original_language", "Original language", "title", "original"),
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
    operators: [...IS_OPERATORS, ...NUMBER_OPERATORS],
    inputType: "number",
    valueType: "number",
    supportsRange: true,
  },
  {
    value: "decade",
    label: "Decade",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "number",
    selectOptions: decadeOptions(),
    extended: true,
  },
  dateField("release_date", "Release date", "title"),
  // A show's newest episode, by when it aired.
  {
    ...dateField("last_air_date", "Latest episode aired", "title"),
    showsOnly: true,
    extended: true,
  },
  {
    // media_items.runtime holds minutes; a title without one matches no bound.
    value: "runtime",
    label: "Duration",
    group: "title",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    unit: "min",
    extended: true,
  },
  ratingField("rating_imdb", "IMDb rating"),
  { ...ratingField("rating_tmdb", "TMDB rating"), extended: true },
  {
    ...ratingField("rating_rt_critic", "RT critic score", "rt_critic"),
    extended: true,
    noEpisodeValue: true,
  },
  {
    ...ratingField("rating_rt_audience", "RT audience score", "rt_audience"),
    extended: true,
    noEpisodeValue: true,
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
  languageField("audio_language", "Audio language", "file", "audio"),
  languageField("subtitle_language", "Subtitle language", "file", "subtitle"),
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
  // For a show, when its most recently finished episode was watched.
  { ...dateField("last_watched", "Last watched", "you"), personalized: true },
  dateField("added_at", "Added", "library"),
  // A show's newest episode, by when its file arrived.
  {
    ...dateField("latest_episode_added", "Latest episode added", "library"),
    showsOnly: true,
    extended: true,
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

/**
 * The fields a rule can pick, given where it is used: personalized fields
 * only where rules resolve per profile, ratings only while their source is
 * shown, and the extended fields and "is not in the last" only on a server
 * that takes them. Unset `shownRatingSources` offers every rating.
 */
export function availableCollectionFields(
  allowPersonalized: boolean,
  shownRatingSources?: ReadonlySet<string>,
  extendedRules = true,
): CollectionFieldOption[] {
  const fields = COLLECTION_FIELD_OPTIONS.filter(
    (option) =>
      (allowPersonalized || !option.personalized) &&
      (!option.ratingSource ||
        !shownRatingSources ||
        shownRatingSources.has(option.ratingSource)) &&
      (extendedRules || !option.extended),
  );
  if (extendedRules) return fields;
  return fields.map((option) => ({
    ...option,
    operators: option.operators.map((op) =>
      op.value === "not_in_last" ? { ...op, hidden: true } : op,
    ),
  }));
}

/** The rule a new line starts with. */
export function newFilterRule(): FilterRule {
  return { field: "genre", op: "is", value: getDefaultRuleValue("genre", "is") };
}
