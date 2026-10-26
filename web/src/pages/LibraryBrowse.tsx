import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";

import { normalizeQueryDefinition, type QueryDefinition } from "@/api/types";
import AudiobookGroupsView from "@/components/audiobooks/AudiobookGroupsView";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import LetterBar from "@/components/catalog/LetterBar";
import { letterAtIndex } from "@/components/catalog/letterIndex";
import ItemGrid, { type ItemGridHandle } from "@/components/ItemGrid";
import LibraryEmptyState from "@/components/LibraryEmptyState";
import { loadErrorDescription } from "@/components/loadErrorDescription";
import PageUnavailable from "@/components/PageUnavailable";
import RefreshFailedNotice from "@/components/RefreshFailedNotice";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import { useCatalogLetters, useCatalogWindow, useLibraryHasItems } from "@/hooks/queries/catalog";
import type { AudiobookGroupBy } from "@/hooks/queries/audiobookGroups";
import { cn } from "@/lib/utils";
import { normalizeQuerySortForScope } from "@/lib/querySortOptions";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  AUDIOBOOK_BROWSE_AXES,
  audiobookBrowseAxisFromBrowseType,
  getLibrarySortRelevanceScope,
  isAudiobookLibraryType,
  isEbookLibraryType,
  isMangaLibraryType,
  type AudiobookBrowseAxis,
  type LibraryBrowseType,
} from "./libraryPageSearchParams";

interface LibraryBrowseProps {
  libraryId: number;
  libraryType: string;
  browseType: LibraryBrowseType;
  queryDefinition: QueryDefinition;
  onBrowseTypeChange: (
    browseType: LibraryBrowseType,
    nextQueryDefinition?: QueryDefinition,
  ) => void;
  onQueryDefinitionChange: (queryDefinition: QueryDefinition) => void;
}

const AXIS_LABELS: Record<AudiobookBrowseAxis, string> = {
  books: "Books",
  series: "Series",
  authors: "Authors",
  narrators: "Narrators",
};

const AXIS_GROUP_BY: Record<Exclude<AudiobookBrowseAxis, "books">, AudiobookGroupBy> = {
  series: "series",
  authors: "author",
  narrators: "narrator",
};

const AXIS_FILTER_FIELD: Record<Exclude<AudiobookBrowseAxis, "books">, string> = {
  series: "series",
  authors: "author",
  narrators: "narrator",
};

function AudiobookAxisTabs({
  value,
  onChange,
}: {
  value: AudiobookBrowseAxis;
  onChange: (axis: AudiobookBrowseAxis) => void;
}) {
  return (
    <div
      role="radiogroup"
      aria-label="Browse audiobooks by"
      className="surface-panel inline-flex items-center gap-1 rounded-full p-1"
    >
      {AUDIOBOOK_BROWSE_AXES.map((axis) => {
        const isActive = axis === value;
        return (
          <button
            key={axis}
            type="button"
            role="radio"
            aria-checked={isActive}
            onClick={() => {
              if (!isActive) {
                onChange(axis);
              }
            }}
            className={cn(
              "rounded-full px-4 py-1.5 text-sm font-medium transition-colors",
              isActive
                ? "bg-primary text-primary-foreground"
                : "text-muted-foreground hover:text-foreground",
            )}
          >
            {AXIS_LABELS[axis]}
          </button>
        );
      })}
    </div>
  );
}

function NoMatchingItems({ onClearFilters }: { onClearFilters?: () => void }) {
  return (
    <div className="text-muted-foreground flex flex-col items-center gap-3 py-12 text-center">
      <p>No items match your current filters.</p>
      {onClearFilters ? (
        <button
          type="button"
          onClick={onClearFilters}
          className="text-primary text-sm font-medium hover:underline"
        >
          Clear filters
        </button>
      ) : null}
    </div>
  );
}

export default function LibraryBrowse({
  libraryId,
  libraryType,
  browseType,
  queryDefinition,
  onBrowseTypeChange,
  onQueryDefinitionChange,
}: LibraryBrowseProps) {
  const limit = 60;
  const sortRelevanceScope =
    browseType === "episode"
      ? "all"
      : getLibrarySortRelevanceScope(libraryType, queryDefinition.media_scope);
  const scopedQueryDefinition = normalizeQueryDefinition({
    ...queryDefinition,
    library_ids: [libraryId],
    media_scope:
      libraryType === "mixed"
        ? queryDefinition.media_scope
        : isAudiobookLibraryType(libraryType)
          ? "audiobook"
          : isEbookLibraryType(libraryType)
            ? "ebook"
            : isMangaLibraryType(libraryType)
              ? "manga"
              : libraryType === "movie"
                ? libraryType
                : undefined,
    sort: normalizeQuerySortForScope(queryDefinition.sort, {
      includePersonalized: true,
      relevanceScope: sortRelevanceScope,
    }),
  });
  const showMediaScopeSelector = libraryType === "mixed";
  const filtersKey = JSON.stringify(scopedQueryDefinition);

  const [visibleRangeState, setVisibleRangeState] = useState<{
    key: string;
    range: [number, number];
  }>({
    key: filtersKey,
    range: [0, limit - 1],
  });
  const visibleRange =
    visibleRangeState.key === filtersKey
      ? visibleRangeState.range
      : ([0, limit - 1] as [number, number]);

  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const handleVisibleRangeChange = useCallback(
    (start: number, end: number) => {
      clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        setVisibleRangeState({ key: filtersKey, range: [start, end] });
      }, 50);
    },
    [filtersKey],
  );

  useEffect(() => () => clearTimeout(debounceRef.current), []);

  const audiobookAxis = isAudiobookLibraryType(libraryType)
    ? audiobookBrowseAxisFromBrowseType(browseType)
    : null;
  const isGroupedAxis = audiobookAxis != null && audiobookAxis !== "books";

  const state: CatalogSearchState = {
    source: "query",
    library_id: libraryId,
    type_override: libraryType === "series" ? browseType : undefined,
    query_definition: scopedQueryDefinition,
  };

  const catalogQuery = useCatalogWindow(state, {
    limit,
    includeTotal: false,
    visibleRange,
    enabled: !isGroupedAxis,
  });
  const totalItems = catalogQuery.data?.totalItems ?? 0;
  const pages = catalogQuery.data?.pages ?? new Map();
  // Title order gets an A–Z bar. The letter index carries each letter's list
  // position and the exact total, so the grid can size itself to the whole
  // result and a jump lands on the letter's first title without loading the
  // titles before it.
  // A capped result (query_limit) has no index: the letters count the whole
  // library.
  const titleOrdered =
    !isGroupedAxis &&
    scopedQueryDefinition.sort.field === "title" &&
    scopedQueryDefinition.limit === undefined;
  const lettersQuery = useCatalogLetters(state, { enabled: titleOrdered });
  const letterIndex = titleOrdered && totalItems > 0 ? lettersQuery.data : undefined;
  const showLetterBar = letterIndex !== undefined && letterIndex.total > 0;
  // The index's exact total replaces the pager's estimated runway. The index
  // and the pages are separate reads, so a scan between them can leave either
  // one a few titles behind; the larger count never hides a title the pages
  // return.
  const gridTotalItems = showLetterBar
    ? Math.max(letterIndex.total, catalogQuery.data?.knownItems ?? 0)
    : totalItems;
  const gridRef = useRef<ItemGridHandle>(null);
  // The last title in the top row: a jump scrolls the letter's first title
  // into that row, often after titles of the letter before it.
  const [topRowEnd, setTopRowEnd] = useState(0);
  const handleTopRowChange = useCallback((_first: number, last: number) => setTopRowEnd(last), []);
  // A letter near the end can't scroll to the top of the viewport, so the top
  // row still belongs to an earlier letter. Keep the chosen letter current
  // while the page stays scrolled to the bottom; any scroll away from it, by
  // hand or by the scroll-to-top button, hands back to the top row.
  const [jumpedLetter, setJumpedLetter] = useState<string>();
  useEffect(() => {
    if (!jumpedLetter) return;
    const releaseUnlessAtBottom = () => {
      const bottomGap =
        document.documentElement.scrollHeight - (window.scrollY + window.innerHeight);
      if (bottomGap > 2) setJumpedLetter(undefined);
    };
    window.addEventListener("scroll", releaseUnlessAtBottom, { passive: true });
    return () => window.removeEventListener("scroll", releaseUnlessAtBottom);
  }, [jumpedLetter]);
  // An empty result only says the current view matched nothing. Ask whether
  // the library holds anything at all before calling it empty, so an
  // over-filtered view keeps pointing at its filters.
  const browseCameBackEmpty =
    !isGroupedAxis && !catalogQuery.isLoading && !catalogQuery.isError && totalItems === 0;
  // The bar holds its place from the first render of a title-ordered browse,
  // greyed until the index lands, so the grid doesn't reflow and the bar
  // doesn't pop in. It steps aside only when there is nothing to index or the
  // index can't be read at all.
  const reservesLetterBar =
    titleOrdered && !browseCameBackEmpty && !(lettersQuery.isError && !lettersQuery.data);
  const libraryHasItemsQuery = useLibraryHasItems(libraryId, { enabled: browseCameBackEmpty });
  const isLoading =
    catalogQuery.isLoading || (browseCameBackEmpty && libraryHasItemsQuery.isLoading);
  const hasClearableFilters =
    queryDefinition.groups.length > 0 ||
    (showMediaScopeSelector && queryDefinition.media_scope !== undefined);
  let emptyState: ReactNode;
  if (browseCameBackEmpty && libraryHasItemsQuery.data === false) {
    emptyState = <LibraryEmptyState libraryId={libraryId} />;
  } else if (browseCameBackEmpty && libraryHasItemsQuery.data === true) {
    emptyState = (
      <NoMatchingItems
        onClearFilters={
          hasClearableFilters
            ? () =>
                onQueryDefinitionChange({
                  ...queryDefinition,
                  library_ids: [],
                  media_scope: showMediaScopeSelector ? undefined : queryDefinition.media_scope,
                  match: "all",
                  groups: [],
                })
            : undefined
        }
      />
    );
  }
  // A failed refresh of the letter index keeps the bar on its cached
  // positions, which may now land a little off, so say so like a failed page.
  const lettersRefreshFailed = showLetterBar && lettersQuery.isRefetchError;
  const [retrying, setRetrying] = useState(false);
  const retryBrowse = () => {
    setRetrying(true);
    void Promise.all([
      catalogQuery.isError || !lettersRefreshFailed ? catalogQuery.refetch() : null,
      lettersRefreshFailed ? lettersQuery.refetch() : null,
    ]).finally(() => setRetrying(false));
  };
  // The first page carries the result window, so without it (or with only an
  // empty one) a failure leaves nothing to show: the browse failed outright
  // and must not read as an empty library. A failed background refetch or a
  // failed later page keeps the loaded grid and the viewer's place in it.
  const browseFailed = catalogQuery.isError && (!pages.has(0) || totalItems === 0);
  const browsePartlyFailed = catalogQuery.isError && !browseFailed;
  let refreshFailedMessage: string | undefined;
  if (browsePartlyFailed) {
    refreshFailedMessage = catalogQuery.sourceError
      ? "Couldn't refresh this library."
      : "Some items couldn't be loaded.";
  } else if (lettersRefreshFailed) {
    refreshFailedMessage = "Couldn't refresh the letter bar, so a jump may land a little off.";
  }

  if (isGroupedAxis) {
    const groupedAxis = audiobookAxis as Exclude<AudiobookBrowseAxis, "books">;
    return (
      <div className="space-y-5 py-2 sm:space-y-6">
        <AudiobookAxisTabs value={groupedAxis} onChange={(axis) => onBrowseTypeChange(axis)} />
        <AudiobookGroupsView
          key={groupedAxis}
          libraryId={libraryId}
          groupBy={AXIS_GROUP_BY[groupedAxis]}
          onSelectGroup={(name) =>
            // Drop into the Books grid filtered to the selected group. The
            // group name round-trips through the matching filter field, which
            // the backend matches case-insensitively.
            onBrowseTypeChange("books", {
              ...queryDefinition,
              library_ids: [],
              groups: [
                {
                  match: "all",
                  rules: [{ field: AXIS_FILTER_FIELD[groupedAxis], op: "is", value: name }],
                },
              ],
            })
          }
        />
        <ScrollToTopButton />
      </div>
    );
  }

  return (
    <div className="space-y-5 py-2 sm:space-y-6">
      {libraryType === "series" ? (
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-muted-foreground text-sm font-medium">Type</span>
          <Select
            value={browseType}
            onValueChange={(value) => onBrowseTypeChange(value as LibraryBrowseType)}
          >
            <SelectTrigger className="w-[180px]">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="series">Series</SelectItem>
              <SelectItem value="episode">Episodes</SelectItem>
            </SelectContent>
          </Select>
        </div>
      ) : null}
      {audiobookAxis != null && (
        <AudiobookAxisTabs value={audiobookAxis} onChange={(axis) => onBrowseTypeChange(axis)} />
      )}
      <CatalogFiltersPanel
        state={state}
        onStateChange={(nextState) =>
          onQueryDefinitionChange({
            ...nextState.query_definition,
            library_ids: [],
          })
        }
        allowLibrarySelection={false}
        showMediaScopeSelector={showMediaScopeSelector}
        allowPersonalizedFilters
        allowPersonalizedSorts
        sortRelevanceScope={sortRelevanceScope}
        libraryType={libraryType}
      />
      {refreshFailedMessage ? (
        <RefreshFailedNotice
          message={refreshFailedMessage}
          error={browsePartlyFailed ? catalogQuery.error : lettersQuery.error}
          onRetry={retryBrowse}
          retrying={retrying}
        />
      ) : null}
      {/* A failed browse is not an empty one: it must never reach the grid's
          empty state. */}
      {browseFailed ? (
        <PageUnavailable
          title="Couldn't load this library"
          description={loadErrorDescription(catalogQuery.error)}
          onRetry={retryBrowse}
          retrying={retrying}
        />
      ) : (
        <div
          className={
            reservesLetterBar ? "pr-6 sm:pr-9 [@media(max-height:599px)]:pr-14" : undefined
          }
        >
          <ItemGrid
            gridRef={gridRef}
            totalItems={gridTotalItems}
            pages={pages}
            pageSize={limit}
            libraryId={libraryId}
            loading={isLoading}
            onVisibleRangeChange={handleVisibleRangeChange}
            onTopRowChange={showLetterBar ? handleTopRowChange : undefined}
            sortField={scopedQueryDefinition.sort.field}
            emptyState={emptyState}
          />
        </div>
      )}
      {reservesLetterBar && !browseFailed ? (
        <LetterBar
          letters={showLetterBar ? letterIndex.letters : undefined}
          activeLetter={
            showLetterBar ? (jumpedLetter ?? letterAtIndex(letterIndex.runs, topRowEnd)) : undefined
          }
          onJump={(letter) => {
            setJumpedLetter(letter.letter);
            gridRef.current?.scrollToItem(letter.position);
          }}
        />
      ) : null}
      <ScrollToTopButton />
    </div>
  );
}
