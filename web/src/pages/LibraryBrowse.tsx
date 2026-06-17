import { useCallback, useEffect, useRef, useState } from "react";

import { normalizeQueryDefinition, type QueryDefinition } from "@/api/types";
import AudiobookGroupsView from "@/components/audiobooks/AudiobookGroupsView";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import ItemGrid from "@/components/ItemGrid";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import { useCatalogWindow } from "@/hooks/queries/catalog";
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
  const isLoading = catalogQuery.isLoading;

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
      <ItemGrid
        totalItems={totalItems}
        pages={pages}
        pageSize={limit}
        libraryId={libraryId}
        loading={isLoading}
        onVisibleRangeChange={handleVisibleRangeChange}
        sortField={scopedQueryDefinition.sort.field}
      />
      <ScrollToTopButton />
    </div>
  );
}
