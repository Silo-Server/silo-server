import { useMemo } from "react";

import {
  normalizeQueryDefinition,
  type CatalogFiltersResponse,
  type QueryDefinition,
  type QueryDefinitionInput,
  type QueryGroup,
  type QueryRule,
} from "@/api/types";
import LibraryMultiSelect from "@/components/LibraryMultiSelect";
import { Button } from "@/components/ui/button";
import { FacetSearchSelect } from "@/components/ui/facet-search-select";
import { PersonSearchSelect } from "@/components/ui/person-search-select";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { SearchableMultiSelect, SearchableSelect } from "@/components/ui/searchable-select";
import { formatLanguage } from "@/lib/languageDisplay";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useCatalogMetadataFilters } from "@/hooks/queries/catalog";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import {
  getDefaultQuerySortOrder,
  normalizeQuerySortForScope,
  type QuerySortRelevanceScope,
} from "@/lib/querySortOptions";
import { cn } from "@/lib/utils";

import { getCollectionSortOptions } from "./collectionBuilderFields";

const DECADE_OPTIONS = Array.from({ length: 15 }, (_, index) => 2030 - index * 10).filter(
  (year) => year >= 1900,
);

/** Flat form state that maps 1-to-1 with friendly form fields. */
export interface GuidedFormState {
  mediaScope: "all" | "movie" | "series" | "episode" | "audiobook" | "ebook";
  libraryIds: number[];
  genres: string[];
  decade: string;
  yearFrom: string;
  yearTo: string;
  minRating: string;
  contentRating: string;
  originalLanguages: string[];
  actor: string;
  director: string;
  writer: string;
  producer: string;
  author: string;
  narrator: string;
  series: string;
  studio: string;
  network: string;
  country: string;
  status: string;
  watchStatus: "" | "watched" | "unwatched" | "in_progress";
  addedInLast: string;
  releasedInLast: string;
  fourK: boolean;
  hdr: boolean;
  dolbyVision: boolean;
  sortField: string;
  sortOrder: "asc" | "desc";
}

function deriveDecade(yearFrom: string, yearTo: string): string {
  const from = Number(yearFrom);
  const to = Number(yearTo);
  if (!Number.isInteger(from) || !Number.isInteger(to)) {
    return "";
  }
  if (from % 10 !== 0 || to !== from + 9) {
    return "";
  }
  return String(from);
}

function isFourKResolution(value: unknown): boolean {
  const normalized = String(value ?? "")
    .trim()
    .toLowerCase();
  return normalized === "2160p" || normalized === "4k" || normalized === "uhd";
}

/** Extract a friendly form state from a QueryDefinition. */
export function queryDefinitionToGuidedState(
  qd: QueryDefinition | QueryDefinitionInput,
): GuidedFormState {
  const normalized = normalizeQueryDefinition(qd);
  const state: GuidedFormState = {
    mediaScope: normalized.media_scope ?? "all",
    libraryIds: [...normalized.library_ids],
    genres: [],
    decade: "",
    yearFrom: "",
    yearTo: "",
    minRating: "",
    contentRating: "",
    originalLanguages: [],
    actor: "",
    director: "",
    writer: "",
    producer: "",
    author: "",
    narrator: "",
    series: "",
    studio: "",
    network: "",
    country: "",
    status: "",
    watchStatus: "",
    addedInLast: "",
    releasedInLast: "",
    fourK: false,
    hdr: false,
    dolbyVision: false,
    sortField: normalized.sort.field,
    sortOrder: normalized.sort.order,
  };

  const allRules: QueryRule[] = normalized.groups.flatMap((group) => group.rules);
  const genreValues: string[] = [];
  let watchedTrue = false;
  let watchedFalse = false;
  let inProgressTrue = false;
  let inProgressFalse = false;

  for (const rule of allRules) {
    switch (rule.field) {
      case "genre":
        if (rule.op === "is" || rule.op === "contains") {
          genreValues.push(String(rule.value));
        }
        break;
      case "year":
        if (rule.op === "between" && Array.isArray(rule.value) && rule.value.length === 2) {
          state.yearFrom = String(rule.value[0] ?? "");
          state.yearTo = String(rule.value[1] ?? "");
          break;
        }
        if (rule.op === "gte" || rule.op === "gt") {
          state.yearFrom = String(rule.value);
        }
        if (rule.op === "lte" || rule.op === "lt") {
          state.yearTo = String(rule.value);
        }
        if (rule.op === "is") {
          state.yearFrom = String(rule.value);
          state.yearTo = String(rule.value);
        }
        break;
      case "rating":
      case "rating_imdb":
        if (rule.op === "gte" || rule.op === "gt") {
          state.minRating = String(rule.value);
        }
        break;
      case "content_rating":
        if (rule.op === "is") state.contentRating = String(rule.value);
        break;
      case "original_language":
        if (rule.op === "is") {
          const lang = String(rule.value);
          if (lang && !state.originalLanguages.includes(lang)) {
            state.originalLanguages.push(lang);
          }
        }
        break;
      case "actor":
        if (rule.op === "is") state.actor = String(rule.value);
        break;
      case "director":
        if (rule.op === "is") state.director = String(rule.value);
        break;
      case "writer":
        if (rule.op === "is") state.writer = String(rule.value);
        break;
      case "producer":
        if (rule.op === "is") state.producer = String(rule.value);
        break;
      case "author":
        if (rule.op === "is") state.author = String(rule.value);
        break;
      case "narrator":
        if (rule.op === "is" && normalized.media_scope === "audiobook") {
          state.narrator = String(rule.value);
        }
        break;
      case "series":
        if (rule.op === "is") state.series = String(rule.value);
        break;
      case "studio":
        if (rule.op === "is") state.studio = String(rule.value);
        break;
      case "network":
        if (rule.op === "is") state.network = String(rule.value);
        break;
      case "country":
        if (rule.op === "is") state.country = String(rule.value);
        break;
      case "status":
        if (rule.op === "is") state.status = String(rule.value);
        break;
      case "added_at":
        if (rule.op === "in_last") state.addedInLast = String(rule.value);
        break;
      case "release_date":
        if (rule.op === "in_last") state.releasedInLast = String(rule.value);
        break;
      case "watched":
        if (rule.op === "is" && rule.value === true) watchedTrue = true;
        if (rule.op === "is" && rule.value === false) watchedFalse = true;
        break;
      case "in_progress":
        if (rule.op === "is" && rule.value === true) inProgressTrue = true;
        if (rule.op === "is" && rule.value === false) inProgressFalse = true;
        break;
      case "resolution":
        if (rule.op === "is" && isFourKResolution(rule.value)) {
          state.fourK = true;
        }
        break;
      case "hdr":
        if (rule.op === "is" && rule.value === true) {
          state.hdr = true;
        }
        break;
      case "dolby_vision":
        if (rule.op === "is" && rule.value === true) {
          state.dolbyVision = true;
        }
        break;
    }
  }

  if (watchedTrue) {
    state.watchStatus = "watched";
  } else if (inProgressTrue) {
    state.watchStatus = "in_progress";
  } else if (watchedFalse && inProgressFalse) {
    state.watchStatus = "unwatched";
  }

  state.genres = genreValues;
  state.decade = deriveDecade(state.yearFrom, state.yearTo);
  return state;
}

/** Build a QueryDefinition from the guided form state. */
export function guidedStateToQueryDefinition(
  state: GuidedFormState,
  existing: QueryDefinition | QueryDefinitionInput,
): QueryDefinition {
  const rules: QueryRule[] = [];

  for (const genre of state.genres) {
    if (genre) {
      rules.push({ field: "genre", op: "is", value: genre });
    }
  }

  if (state.yearFrom) {
    rules.push({ field: "year", op: "gte", value: Number(state.yearFrom) });
  }
  if (state.yearTo) {
    rules.push({ field: "year", op: "lte", value: Number(state.yearTo) });
  }

  if (state.minRating) {
    rules.push({ field: "rating_imdb", op: "gte", value: Number(state.minRating) });
  }

  if (state.contentRating) {
    rules.push({ field: "content_rating", op: "is", value: state.contentRating });
  }
  if (state.originalLanguages.length === 1) {
    // Single language stays inline so the AND group still satisfies; multi
    // is split into its own OR group below since an item has one language.
    rules.push({ field: "original_language", op: "is", value: state.originalLanguages[0]! });
  }
  if (state.actor) {
    rules.push({ field: "actor", op: "is", value: state.actor });
  }
  if (state.director) {
    rules.push({ field: "director", op: "is", value: state.director });
  }
  if (state.writer) {
    rules.push({ field: "writer", op: "is", value: state.writer });
  }
  if (state.producer) {
    rules.push({ field: "producer", op: "is", value: state.producer });
  }
  if (state.author) {
    rules.push({ field: "author", op: "is", value: state.author });
  }
  if (state.narrator && state.mediaScope === "audiobook") {
    rules.push({ field: "narrator", op: "is", value: state.narrator });
  }
  if (state.series) {
    rules.push({ field: "series", op: "is", value: state.series });
  }
  if (state.studio) {
    rules.push({ field: "studio", op: "is", value: state.studio });
  }
  if (state.network) {
    rules.push({ field: "network", op: "is", value: state.network });
  }
  if (state.country) {
    rules.push({ field: "country", op: "is", value: state.country });
  }
  if (state.status) {
    rules.push({ field: "status", op: "is", value: state.status });
  }

  if (state.watchStatus === "watched") {
    rules.push({ field: "watched", op: "is", value: true });
  } else if (state.watchStatus === "in_progress") {
    rules.push({ field: "in_progress", op: "is", value: true });
  } else if (state.watchStatus === "unwatched") {
    rules.push({ field: "watched", op: "is", value: false });
    rules.push({ field: "in_progress", op: "is", value: false });
  }

  if (state.addedInLast) {
    rules.push({ field: "added_at", op: "in_last", value: state.addedInLast });
  }
  if (state.releasedInLast) {
    rules.push({ field: "release_date", op: "in_last", value: state.releasedInLast });
  }

  if (state.fourK) {
    rules.push({ field: "resolution", op: "is", value: "2160p" });
  }
  if (state.hdr) {
    rules.push({ field: "hdr", op: "is", value: true });
  }
  if (state.dolbyVision) {
    rules.push({ field: "dolby_vision", op: "is", value: true });
  }

  const groups: QueryGroup[] = [];
  if (rules.length > 0) {
    groups.push({ match: "all", rules });
  }
  if (state.originalLanguages.length > 1) {
    groups.push({
      match: "any",
      rules: state.originalLanguages.map((lang) => ({
        field: "original_language",
        op: "is",
        value: lang,
      })),
    });
  }

  return normalizeQueryDefinition({
    library_ids: state.libraryIds,
    media_scope: state.mediaScope === "all" ? undefined : state.mediaScope,
    match: "all",
    groups,
    sort: {
      field: state.sortField as QueryDefinition["sort"]["field"],
      order: state.sortOrder,
    },
    limit: existing.limit,
  });
}

interface CollectionGuidedRulesEditorProps {
  value: QueryDefinition;
  onChange: (value: QueryDefinition) => void;
  libraries?: Array<{ id: number; name: string }>;
  allowLibrarySelection?: boolean;
  showMediaScopeSelector?: boolean;
  allowPersonalizedFilters?: boolean;
  allowPersonalizedSorts?: boolean;
  sortRelevanceScope?: QuerySortRelevanceScope;
  readOnly?: boolean;
  showSortControls?: boolean;
  filters?: CatalogFiltersResponse;
  filtersLoading?: boolean;
  // When the editor is scoped to a single library (e.g. on the library
  // browse page) pass its type so video-only filter sections (Director,
  // Studio, Network, Content Rating, Video Quality, etc.) can be hidden
  // for book libraries.
  libraryType?: string;
  // When set, the book-native facet sections (Author / Series, and
  // audiobook-only Narrator) switch to typeahead-backed FacetSearchSelect, querying
  // /api/v1/catalog/filters/search scoped to this state. Without it
  // they fall back to the bulk filters payload (top 1000 alphabetical).
  catalogState?: CatalogSearchState;
}

export default function CollectionGuidedRulesEditor({
  value,
  onChange,
  libraries = [],
  allowLibrarySelection = true,
  showMediaScopeSelector = true,
  allowPersonalizedFilters = false,
  allowPersonalizedSorts = false,
  sortRelevanceScope,
  readOnly = false,
  showSortControls = true,
  filters: providedFilters,
  filtersLoading: providedFiltersLoading,
  libraryType,
  catalogState,
}: CollectionGuidedRulesEditorProps) {
  const state = useMemo(() => queryDefinitionToGuidedState(value), [value]);
  const metadataFiltersQuery = useCatalogMetadataFilters();
  const filters = providedFilters ?? metadataFiltersQuery.data;
  const filtersLoading = providedFiltersLoading ?? metadataFiltersQuery.isLoading;
  // Backend stores book library types as plurals, while some API surfaces
  // use singular media scopes; accept both.
  const isAudiobookLibrary =
    libraryType === "audiobook" ||
    libraryType === "audiobooks" ||
    state.mediaScope === "audiobook";
  const isEbookLibrary =
    libraryType === "ebook" || libraryType === "ebooks" || state.mediaScope === "ebook";
  const isBookLibrary = isAudiobookLibrary || isEbookLibrary;
  const sortOptions = getCollectionSortOptions(allowPersonalizedSorts, sortRelevanceScope);
  const selectedSort = normalizeQuerySortForScope(
    { field: state.sortField, order: state.sortOrder },
    { includePersonalized: allowPersonalizedSorts, relevanceScope: sortRelevanceScope },
  );

  function update(patch: Partial<GuidedFormState>) {
    const next = { ...state, ...patch };
    next.decade = deriveDecade(next.yearFrom, next.yearTo);
    onChange(guidedStateToQueryDefinition(next, value));
  }

  const languageOptions = filters?.original_languages ?? [];

  function updateDecade(nextDecade: string) {
    if (!nextDecade) {
      update({ decade: "", yearFrom: "", yearTo: "" });
      return;
    }
    const startYear = Number(nextDecade);
    update({
      decade: nextDecade,
      yearFrom: String(startYear),
      yearTo: String(startYear + 9),
    });
  }

  return (
    <div className="space-y-4">
      {showMediaScopeSelector || allowLibrarySelection ? (
        <div
          className={cn(
            "grid gap-4",
            showMediaScopeSelector && allowLibrarySelection ? "md:grid-cols-2" : undefined,
          )}
        >
          {showMediaScopeSelector ? (
            <div className="space-y-2">
              <Label>Media Type</Label>
              <Select
                value={state.mediaScope}
                onValueChange={(v) => {
                  const nextRelevanceScope = v === "all" ? "all" : (v as QuerySortRelevanceScope);
                  const nextSort = normalizeQuerySortForScope(
                    { field: state.sortField, order: state.sortOrder },
                    {
                      includePersonalized: allowPersonalizedSorts,
                      relevanceScope: nextRelevanceScope,
                    },
                  );
                  update({
                    mediaScope: v as GuidedFormState["mediaScope"],
                    sortField: nextSort.field,
                    sortOrder: nextSort.order,
                  });
                }}
                disabled={readOnly}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">All Media</SelectItem>
                  <SelectItem value="movie">Movies</SelectItem>
                  <SelectItem value="series">Series</SelectItem>
                  <SelectItem value="episode">Episodes</SelectItem>
                  <SelectItem value="audiobook">Audiobooks</SelectItem>
                  <SelectItem value="ebook">Ebooks</SelectItem>
                </SelectContent>
              </Select>
            </div>
          ) : null}

          {allowLibrarySelection ? (
            <div className="space-y-2">
              <Label>Libraries</Label>
              <LibraryMultiSelect
                libraries={libraries}
                value={state.libraryIds}
                onChange={(libraryIds) => update({ libraryIds })}
              />
            </div>
          ) : null}
        </div>
      ) : null}

      <div className="space-y-2">
        <Label>Genres</Label>
        <SearchableMultiSelect
          options={filters?.genres ?? []}
          value={state.genres}
          onChange={(genres) => update({ genres })}
          placeholder="Select genres..."
          disabled={readOnly}
          isLoading={filtersLoading}
        />
        <p className="text-muted-foreground text-xs">Items must match all selected genres.</p>
      </div>

      <div className="grid gap-4 md:grid-cols-3">
        <div className="space-y-2">
          <Label>Decade</Label>
          <Select
            value={state.decade || "__custom__"}
            onValueChange={(value) => updateDecade(value === "__custom__" ? "" : value)}
            disabled={readOnly}
          >
            <SelectTrigger>
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__custom__">Custom</SelectItem>
              {DECADE_OPTIONS.map((year) => (
                <SelectItem key={year} value={String(year)}>
                  {year}s
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label>Year From</Label>
          <Input
            type="number"
            value={state.yearFrom}
            onChange={(e) => update({ yearFrom: e.target.value, decade: "" })}
            placeholder="e.g. 2000"
            disabled={readOnly}
          />
        </div>
        <div className="space-y-2">
          <Label>Year To</Label>
          <Input
            type="number"
            value={state.yearTo}
            onChange={(e) => update({ yearTo: e.target.value, decade: "" })}
            placeholder="e.g. 2025"
            disabled={readOnly}
          />
        </div>
      </div>

      {isBookLibrary ? null : (
        <div className="grid gap-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Minimum IMDb Rating</Label>
            <Input
              type="number"
              min={0}
              max={10}
              step={0.1}
              value={state.minRating}
              onChange={(e) => update({ minRating: e.target.value })}
              placeholder="e.g. 7.0"
              disabled={readOnly}
            />
          </div>
          <div className="space-y-2">
            <Label>Content Rating</Label>
            <SearchableSelect
              options={filters?.content_ratings ?? []}
              value={state.contentRating}
              onChange={(contentRating) => update({ contentRating })}
              placeholder="Select rating..."
              disabled={readOnly}
              isLoading={filtersLoading}
            />
          </div>
        </div>
      )}

      <div className="space-y-2">
        <Label>Original Language</Label>
        <SearchableMultiSelect
          options={languageOptions}
          value={state.originalLanguages}
          onChange={(originalLanguages) => update({ originalLanguages })}
          placeholder="Select languages..."
          disabled={readOnly}
          isLoading={filtersLoading}
          getLabel={formatLanguage}
        />
      </div>

      {isBookLibrary ? null : (
        <>
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Actor</Label>
              <PersonSearchSelect
                value={state.actor}
                onChange={(actor) => update({ actor })}
                disabled={readOnly}
              />
            </div>
            <div className="space-y-2">
              <Label>Director</Label>
              <PersonSearchSelect
                value={state.director}
                onChange={(director) => update({ director })}
                disabled={readOnly}
              />
            </div>
          </div>

          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Writer</Label>
              <PersonSearchSelect
                value={state.writer}
                onChange={(writer) => update({ writer })}
                disabled={readOnly}
              />
            </div>
            <div className="space-y-2">
              <Label>Producer</Label>
              <PersonSearchSelect
                value={state.producer}
                onChange={(producer) => update({ producer })}
                disabled={readOnly}
              />
            </div>
          </div>

          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Studio</Label>
              <SearchableSelect
                options={filters?.studios ?? []}
                value={state.studio}
                onChange={(studio) => update({ studio })}
                placeholder="Select studio..."
                disabled={readOnly}
                isLoading={filtersLoading}
              />
            </div>
            <div className="space-y-2">
              <Label>Network</Label>
              <SearchableSelect
                options={filters?.networks ?? []}
                value={state.network}
                onChange={(network) => update({ network })}
                placeholder="Select network..."
                disabled={readOnly}
                isLoading={filtersLoading}
              />
            </div>
          </div>
        </>
      )}

      {isBookLibrary ? (
        <>
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Author</Label>
              {catalogState ? (
                <FacetSearchSelect
                  facet="author"
                  state={catalogState}
                  value={state.author}
                  onChange={(author) => update({ author })}
                  placeholder="Search authors..."
                  disabled={readOnly}
                />
              ) : (
                <SearchableSelect
                  options={filters?.authors ?? []}
                  value={state.author}
                  onChange={(author) => update({ author })}
                  placeholder="Select author..."
                  disabled={readOnly}
                  isLoading={filtersLoading}
                />
              )}
            </div>
            {isAudiobookLibrary ? (
              <div className="space-y-2">
                <Label>Narrator</Label>
                {catalogState ? (
                  <FacetSearchSelect
                    facet="narrator"
                    state={catalogState}
                    value={state.narrator}
                    onChange={(narrator) => update({ narrator })}
                    placeholder="Search narrators..."
                    disabled={readOnly}
                  />
                ) : (
                  <SearchableSelect
                    options={filters?.narrators ?? []}
                    value={state.narrator}
                    onChange={(narrator) => update({ narrator })}
                    placeholder="Select narrator..."
                    disabled={readOnly}
                    isLoading={filtersLoading}
                  />
                )}
              </div>
            ) : null}
          </div>
          <div className="grid gap-4 md:grid-cols-2">
            <div className="space-y-2">
              <Label>Series</Label>
              {catalogState ? (
                <FacetSearchSelect
                  facet="series"
                  state={catalogState}
                  value={state.series}
                  onChange={(series) => update({ series })}
                  placeholder="Search series..."
                  disabled={readOnly}
                />
              ) : (
                <SearchableSelect
                  options={filters?.series ?? []}
                  value={state.series}
                  onChange={(series) => update({ series })}
                  placeholder="Select series..."
                  disabled={readOnly}
                  isLoading={filtersLoading}
                />
              )}
            </div>
          </div>
        </>
      ) : null}

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label>Country</Label>
          <SearchableSelect
            options={filters?.countries ?? []}
            value={state.country}
            onChange={(country) => update({ country })}
            placeholder="Select country..."
            disabled={readOnly}
            isLoading={filtersLoading}
          />
        </div>
      </div>

      <div className={cn("grid gap-4", allowPersonalizedFilters ? "md:grid-cols-2" : undefined)}>
        <div className="space-y-2">
          <Label>Match Status</Label>
          <Select
            value={state.status || "__any__"}
            onValueChange={(v) => update({ status: v === "__any__" ? "" : v })}
            disabled={readOnly}
          >
            <SelectTrigger>
              <SelectValue placeholder="Any" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__any__">Any</SelectItem>
              <SelectItem value="matched">Matched</SelectItem>
              <SelectItem value="unmatched">Unmatched</SelectItem>
              <SelectItem value="pending">Pending</SelectItem>
            </SelectContent>
          </Select>
        </div>

        {allowPersonalizedFilters ? (
          <div className="space-y-2">
            <Label>Watch Status</Label>
            <Select
              value={state.watchStatus || "__any__"}
              onValueChange={(value) =>
                update({
                  watchStatus: value === "__any__" ? "" : (value as GuidedFormState["watchStatus"]),
                })
              }
              disabled={readOnly}
            >
              <SelectTrigger>
                <SelectValue placeholder="Any" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="__any__">Any</SelectItem>
                <SelectItem value="watched">Watched</SelectItem>
                <SelectItem value="unwatched">Unwatched</SelectItem>
                <SelectItem value="in_progress">In Progress</SelectItem>
              </SelectContent>
            </Select>
          </div>
        ) : null}
      </div>

      {isBookLibrary ? null : (
        <div className="space-y-2">
          <Label>Video Quality</Label>
          <div className="flex flex-wrap gap-2">
            {[
              { key: "fourK", label: "4K" },
              { key: "hdr", label: "HDR" },
              { key: "dolbyVision", label: "DOVI" },
            ].map((option) => {
              const selected = state[option.key as keyof GuidedFormState] === true;
              return (
                <Button
                  key={option.key}
                  type="button"
                  variant={selected ? "default" : "outline"}
                  size="sm"
                  onClick={() =>
                    update({
                      [option.key]: !selected,
                    } as Partial<GuidedFormState>)
                  }
                  disabled={readOnly}
                >
                  {option.label}
                </Button>
              );
            })}
          </div>
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2">
        <div className="space-y-2">
          <Label>Added in the Last</Label>
          <Input
            value={state.addedInLast}
            onChange={(e) => update({ addedInLast: e.target.value })}
            placeholder="e.g. 30d, 2w, 6m"
            disabled={readOnly}
          />
        </div>
        <div className="space-y-2">
          <Label>Released in the Last</Label>
          <Input
            value={state.releasedInLast}
            onChange={(e) => update({ releasedInLast: e.target.value })}
            placeholder="e.g. 90d, 1y"
            disabled={readOnly}
          />
        </div>
      </div>

      {showSortControls ? (
        <div className="border-border grid gap-4 border-t pt-4 md:grid-cols-2">
          <div className="space-y-2">
            <Label>Sort By</Label>
            <Select
              value={selectedSort.field}
              onValueChange={(v) =>
                update({ sortField: v, sortOrder: getDefaultQuerySortOrder(v) })
              }
              disabled={readOnly}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {sortOptions.map((opt) => (
                  <SelectItem key={opt.value} value={opt.value}>
                    {opt.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>Order</Label>
            <Select
              value={state.sortOrder}
              onValueChange={(v) => update({ sortOrder: v as "asc" | "desc" })}
              disabled={readOnly}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="desc">Descending</SelectItem>
                <SelectItem value="asc">Ascending</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>
      ) : null}
    </div>
  );
}
