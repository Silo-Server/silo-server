import type { PersonalizedSorts } from "@/lib/querySortOptions";
import type { FilterRule, QuerySort } from "@/api/types";
import { getQuerySortOptions, type QuerySortRelevanceScope } from "@/lib/querySortOptions";
import { t, type MessageKey } from "@/i18n";

export interface CollectionOperatorOption {
  value: string;
  label: string;
  /** Kept for saved rules that use it, but not offered for a new one. */
  hidden?: boolean;
}

/** Where a field sits in the field picker. */
export type CollectionFieldGroup = "title" | "people" | "file" | "you" | "library";

/**
 * Text below is read through getters, so each read is in the current language;
 * spreading a field or option copies its label in the language of that moment.
 */
function groupEntry(group: CollectionFieldGroup, key: MessageKey): [CollectionFieldGroup, string] {
  const entry = [group] as unknown as [CollectionFieldGroup, string];
  Object.defineProperty(entry, 1, { enumerable: true, get: () => t(key) });
  return entry;
}

export const COLLECTION_FIELD_GROUPS: ReadonlyArray<[CollectionFieldGroup, string]> = [
  groupEntry("title", "web.collections.builder.groupTitle"),
  groupEntry("people", "web.collections.builder.groupPeople"),
  groupEntry("file", "web.collections.builder.groupFile"),
  groupEntry("you", "web.collections.builder.groupYou"),
  groupEntry("library", "web.collections.builder.groupLibrary"),
];

/** The catalog facets a rule value can be picked from. */
export type CollectionValueFacet = "genre" | "studio" | "network" | "country" | "content_rating";

/** Which of a title's languages a language rule compares. */
export type CollectionLanguageSource = "original" | "audio" | "subtitle";

export interface QueryRuleCapabilities {
  extendedRules?: boolean;
  showTitleRules?: boolean;
}

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
  /** Describes an episode’s parent show. */
  episodesOnly?: boolean;
  /** Taken only by a server that advertises `extended_query_rules`. */
  extended?: boolean;
  /** Episodes carry no value for it, so it is not offered where only episodes match. */
  noEpisodeValue?: boolean;
}

/** A value and its label, read in the current language. */
function option(value: string, key: MessageKey, hidden?: boolean): CollectionOperatorOption {
  return {
    value,
    get label() {
      return t(key);
    },
    ...(hidden ? { hidden } : {}),
  };
}

const IS_OPERATORS: CollectionOperatorOption[] = [
  option("is", "web.collections.builder.opIs"),
  option("is_not", "web.collections.builder.opIsNot"),
];

const NUMBER_OPERATORS: CollectionOperatorOption[] = [
  option("gte", "web.collections.builder.opAtLeast"),
  option("lte", "web.collections.builder.opAtMost"),
  option("gt", "web.collections.builder.opAbove"),
  option("lt", "web.collections.builder.opBelow"),
  option("between", "web.collections.builder.opBetween"),
];

/** Whole or part of a free-text value, compared ignoring case. */
const TEXT_OPERATORS: CollectionOperatorOption[] = [
  option("contains", "web.collections.builder.opContains"),
  option("not_contains", "web.collections.builder.opNotContains"),
  option("is", "web.collections.builder.opIs"),
  option("is_not", "web.collections.builder.opIsNot"),
  option("begins_with", "web.collections.builder.opBeginsWith"),
  option("ends_with", "web.collections.builder.opEndsWith"),
];

/** A span ending today first, since rows and collections mostly want recent titles. */
const DATE_OPERATORS: CollectionOperatorOption[] = [
  option("in_last", "web.collections.builder.opInLast"),
  option("not_in_last", "web.collections.builder.opNotInLast"),
  option("lt", "web.collections.builder.opBefore"),
  option("gt", "web.collections.builder.opAfter"),
  option("between", "web.collections.builder.opBetween"),
];

/** The conditions that take a span such as "30d" rather than a date. */
export const RELATIVE_DATE_OPERATORS: ReadonlySet<string> = new Set(["in_last", "not_in_last"]);

/** Decades from this one back to the 1900s, as the decade's first year. */
function decadeOptions(): Array<{ value: string; label: string }> {
  const latest = Math.floor(new Date().getFullYear() / 10) * 10;
  const options = [];
  for (let decade = latest; decade >= 1900; decade -= 10) {
    options.push({
      value: String(decade),
      get label() {
        return t("web.collections.guidedRules.decadeOption", { decade: decade });
      },
    });
  }
  return options;
}

const SWITCH_OPERATORS: CollectionOperatorOption[] = [option("is", "web.collections.builder.opIs")];

/** A field whose label (and extra props) are read in the current language. */
function field(
  labelKey: MessageKey,
  props: Omit<CollectionFieldOption, "label">,
): CollectionFieldOption {
  return Object.defineProperty(props, "label", {
    enumerable: true,
    get: () => t(labelKey),
  }) as CollectionFieldOption;
}

function facetField(
  value: CollectionValueFacet,
  label: MessageKey,
  operators = IS_OPERATORS,
): CollectionFieldOption {
  return field(label, {
    value,
    group: "title",
    operators,
    inputType: "facet",
    valueType: "string",
    facet: value,
    get placeholder() {
      return t("web.collections.builder.facetPlaceholder", { facet: value });
    },
    get searchLabel() {
      return t("web.collections.builder.facetSearchLabel", { facet: value });
    },
  });
}

function personField(value: string, label: MessageKey): CollectionFieldOption {
  return field(label, {
    value,
    group: "people",
    operators: IS_OPERATORS,
    inputType: "person_search",
    valueType: "string",
  });
}

function dateField(
  value: string,
  label: MessageKey,
  group: CollectionFieldGroup,
  extra: Partial<CollectionFieldOption> = {},
): CollectionFieldOption {
  return field(label, {
    value,
    group,
    operators: DATE_OPERATORS,
    inputType: "date",
    valueType: "string",
    supportsRange: true,
    ...extra,
  });
}

function ratingField(
  value: string,
  label: MessageKey,
  ratingSource?: string,
  extra: Partial<CollectionFieldOption> = {},
): CollectionFieldOption {
  return field(label, {
    value,
    group: "title",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    ...(ratingSource ? { ratingSource } : {}),
    ...extra,
  });
}

function languageField(
  value: string,
  label: MessageKey,
  group: CollectionFieldGroup,
  languageSource: CollectionLanguageSource,
): CollectionFieldOption {
  return field(label, {
    value,
    group,
    operators: IS_OPERATORS,
    inputType: "language",
    valueType: "string",
    languageSource,
  });
}

function switchField(
  value: string,
  label: MessageKey,
  group: CollectionFieldGroup,
  personalized = false,
): CollectionFieldOption {
  return field(label, {
    value,
    group,
    operators: SWITCH_OPERATORS,
    inputType: "boolean",
    valueType: "boolean",
    ...(personalized ? { personalized } : {}),
  });
}

/** In field-picker order: grouped Title, People, File, You, Library. */
export const COLLECTION_FIELD_OPTIONS: CollectionFieldOption[] = [
  field("web.collections.builder.fieldTitle", {
    value: "title",
    group: "title",
    operators: TEXT_OPERATORS,
    inputType: "text",
    valueType: "string",
    extended: true,
  }),
  field("web.collections.builder.fieldShowTitle", {
    value: "show_title",
    group: "title",
    operators: TEXT_OPERATORS,
    inputType: "text",
    valueType: "string",
    episodesOnly: true,
  }),
  facetField("genre", "web.collections.builder.fieldGenre", [
    ...IS_OPERATORS,
    option("contains", "web.collections.builder.opContains", true),
  ]),
  facetField("studio", "web.collections.builder.fieldStudio"),
  facetField("network", "web.collections.builder.fieldNetwork"),
  facetField("country", "web.collections.builder.fieldCountry"),
  facetField("content_rating", "web.collections.builder.fieldContentRating"),
  languageField(
    "original_language",
    "web.collections.builder.fieldOriginalLanguage",
    "title",
    "original",
  ),
  field("web.collections.builder.fieldType", {
    value: "type",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: [
      option("movie", "web.collections.builder.typeMovies"),
      option("series", "web.collections.builder.typeShows"),
    ],
  }),
  field("web.collections.builder.fieldYear", {
    value: "year",
    group: "title",
    operators: [...IS_OPERATORS, ...NUMBER_OPERATORS],
    inputType: "number",
    valueType: "number",
    supportsRange: true,
  }),
  field("web.collections.builder.fieldDecade", {
    value: "decade",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "number",
    selectOptions: decadeOptions(),
    extended: true,
  }),
  dateField("release_date", "web.collections.builder.fieldReleaseDate", "title"),
  // A show's newest episode, by when it aired.
  dateField("last_air_date", "web.collections.builder.fieldLastAirDate", "title", {
    showsOnly: true,
    extended: true,
  }),
  // media_items.runtime holds minutes; a title without one matches no bound.
  field("web.collections.builder.fieldRuntime", {
    value: "runtime",
    group: "title",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    get unit() {
      return t("web.collections.builder.unitMinutes");
    },
    extended: true,
  }),
  ratingField("rating_imdb", "web.collections.builder.fieldRatingImdb"),
  ratingField("rating_tmdb", "web.collections.builder.fieldRatingTmdb", undefined, {
    extended: true,
  }),
  ratingField("rating_rt_critic", "web.collections.builder.fieldRatingRtCritic", "rt_critic", {
    extended: true,
    noEpisodeValue: true,
  }),
  ratingField(
    "rating_rt_audience",
    "web.collections.builder.fieldRatingRtAudience",
    "rt_audience",
    { extended: true, noEpisodeValue: true },
  ),
  // The metadata match state, which only explains a saved rule.
  field("web.collections.builder.fieldStatus", {
    value: "status",
    group: "title",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: [
      option("pending", "web.collections.builder.statusPending"),
      option("matched", "web.collections.builder.statusMatched"),
      option("unmatched", "web.collections.builder.statusUnmatched"),
    ],
    hidden: true,
  }),
  personField("actor", "web.collections.builder.fieldActor"),
  personField("director", "web.collections.builder.fieldDirector"),
  personField("writer", "web.collections.builder.fieldWriter"),
  personField("producer", "web.collections.builder.fieldProducer"),
  field("web.collections.builder.fieldResolution", {
    value: "resolution",
    group: "file",
    operators: IS_OPERATORS,
    inputType: "select",
    valueType: "string",
    selectOptions: ["480p", "720p", "1080p", "2160p", "4320p"].map((value) => ({
      value,
      label: value,
    })),
  }),
  switchField("hdr", "web.collections.builder.fieldHdr", "file"),
  switchField("dolby_vision", "web.collections.builder.fieldDolbyVision", "file"),
  languageField("audio_language", "web.collections.builder.fieldAudioLanguage", "file", "audio"),
  languageField(
    "subtitle_language",
    "web.collections.builder.fieldSubtitleLanguage",
    "file",
    "subtitle",
  ),
  // media_files.bitrate holds kilobits per second.
  field("web.collections.builder.fieldBitrate", {
    value: "bitrate",
    group: "file",
    operators: NUMBER_OPERATORS,
    inputType: "number",
    valueType: "number",
    supportsRange: true,
    get unit() {
      return t("web.collections.builder.unitKbps");
    },
  }),
  switchField("watched", "web.collections.builder.fieldWatched", "you", true),
  switchField("favorited", "web.collections.builder.fieldFavorited", "you", true),
  switchField("in_watchlist", "web.collections.builder.fieldInWatchlist", "you", true),
  switchField("in_progress", "web.collections.builder.fieldInProgress", "you", true),
  // For a show, when its most recently finished episode was watched.
  dateField("last_watched", "web.collections.builder.fieldLastWatched", "you", {
    personalized: true,
  }),
  dateField("added_at", "web.collections.builder.fieldAdded", "library"),
  // A show's newest episode, by when its file arrived.
  dateField("latest_episode_added", "web.collections.builder.fieldLatestEpisodeAdded", "library", {
    showsOnly: true,
    extended: true,
  }),
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
  { extendedRules = true, showTitleRules = false }: QueryRuleCapabilities = {},
): CollectionFieldOption[] {
  const fields = COLLECTION_FIELD_OPTIONS.filter(
    (option) =>
      (allowPersonalized || !option.personalized) &&
      (!option.ratingSource ||
        !shownRatingSources ||
        shownRatingSources.has(option.ratingSource)) &&
      (extendedRules || !option.extended) &&
      (showTitleRules || !option.episodesOnly),
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
