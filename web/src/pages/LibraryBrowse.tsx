import { useCallback, useEffect, useRef, useState } from "react";

import { normalizeQueryDefinition, type QueryDefinition } from "@/api/types";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import ItemGrid from "@/components/ItemGrid";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import { useCatalogWindow } from "@/hooks/queries/catalog";
import { normalizeQuerySortForScope, type QuerySortRelevanceScope } from "@/lib/querySortOptions";
import type { CatalogSearchState } from "@/pages/catalogSearchParams";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import type { LibraryBrowseType } from "./libraryPageSearchParams";

interface LibraryBrowseProps {
  libraryId: number;
  libraryType: string;
  browseType: LibraryBrowseType;
  queryDefinition: QueryDefinition;
  onBrowseTypeChange: (browseType: LibraryBrowseType) => void;
  onQueryDefinitionChange: (queryDefinition: QueryDefinition) => void;
}

function getLibrarySortRelevanceScope(
  libraryType: string,
  mediaScope?: QueryDefinition["media_scope"],
): QuerySortRelevanceScope {
  if (libraryType === "movie" || libraryType === "series") {
    return libraryType;
  }
  if (mediaScope === "movie" || mediaScope === "series" || mediaScope === "episode") {
    return mediaScope;
  }
  return "all";
}

function formatItemCount(count: number): string {
  return `${count.toLocaleString()} item${count === 1 ? "" : "s"}`;
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
  });
  const totalItems = catalogQuery.data?.totalItems ?? 0;
  const pages = catalogQuery.data?.pages ?? new Map();
  const isLoading = catalogQuery.isLoading;
  const itemCountLabel = formatItemCount(totalItems);

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
        resultCountLabel={itemCountLabel}
        resultCountLoading={isLoading}
      />
      <ItemGrid
        totalItems={totalItems}
        pages={pages}
        pageSize={limit}
        loading={isLoading}
        onVisibleRangeChange={handleVisibleRangeChange}
        sortField={scopedQueryDefinition.sort.field}
      />
      <ScrollToTopButton />
    </div>
  );
}
