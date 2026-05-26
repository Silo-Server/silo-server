import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { CheckSquare, Search, Trash2, X } from "lucide-react";

import type { BrowseItem } from "@/api/types";
import ItemGrid from "@/components/ItemGrid";
import { RequestToAddSection } from "@/components/RequestToAddSection";
import { Button } from "@/components/ui/button";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import { useCatalogWindow } from "@/hooks/queries/catalog";
import { useRemoveHistory } from "@/hooks/queries/history";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useDebounce } from "@/hooks/useDebounce";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import SearchBar from "@/components/SearchBar";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import {
  buildHistoryRemovalTarget,
  historyRemovalDialogDescription,
  historyRemovalDialogTitle,
} from "@/lib/historyRemoval";

import { buildCatalogApiSearchParams, parseCatalogSearchParams } from "./catalogSearchParams";

function defaultCatalogTitle(source: string, searchQuery?: string) {
  if (source === "favorites") return "Favorites";
  if (source === "watchlist") return "Watchlist";
  if (source === "history") return "History";
  if (searchQuery) return `Results for "${searchQuery}"`;
  return "Catalog";
}

export default function Catalog() {
  const [searchParams, setSearchParams] = useSearchParams();
  const state = useMemo(() => parseCatalogSearchParams(searchParams), [searchParams]);
  const emptySearchTitle =
    state.source === "query" && !state.q ? "Search" : defaultCatalogTitle(state.source, state.q);

  useDocumentTitle(emptySearchTitle);

  if (state.source === "query" && !state.q) {
    return (
      <section className="page-shell flex min-h-[calc(100dvh-10rem)] flex-col items-center justify-center py-16 text-center">
        <div className="text-muted-foreground mb-6">
          <Search className="h-10 w-10" strokeWidth={1.5} />
        </div>
        <h1 className="page-title mb-4">Search</h1>
        <p className="page-subtitle mb-8 max-w-xl text-sm sm:text-base">
          Find films, series, performances, and rediscover things you forgot you saved.
        </p>
        <SearchBar autoFocus prominent />
      </section>
    );
  }

  return (
    <CatalogResults searchParams={searchParams} setSearchParams={setSearchParams} state={state} />
  );
}

function CatalogResults({
  searchParams,
  setSearchParams,
  state,
}: {
  searchParams: URLSearchParams;
  setSearchParams: (nextInit: URLSearchParams) => void;
  state: ReturnType<typeof parseCatalogSearchParams>;
}) {
  const limit = 60;
  const searchKey = searchParams.toString();
  const [visibleRangeState, setVisibleRangeState] = useState<{
    key: string;
    range: [number, number];
  }>({
    key: searchKey,
    range: [0, limit - 1],
  });
  const visibleRange =
    visibleRangeState.key === searchKey
      ? visibleRangeState.range
      : ([0, limit - 1] as [number, number]);
  const debounceRef = useRef<ReturnType<typeof setTimeout>>(undefined);
  const isHistorySource = state.source === "history";
  const removeHistory = useRemoveHistory();
  const [selectionMode, setSelectionMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [removeConfirmOpen, setRemoveConfirmOpen] = useState(false);
  const handleVisibleRangeChange = useCallback(
    (start: number, end: number) => {
      clearTimeout(debounceRef.current);
      debounceRef.current = setTimeout(() => {
        setVisibleRangeState({ key: searchKey, range: [start, end] });
      }, 50);
    },
    [searchKey],
  );

  useEffect(() => () => clearTimeout(debounceRef.current), []);

  const showExactResultCount = state.source !== "section";
  const catalogQuery = useCatalogWindow(state, {
    limit,
    visibleRange,
    includeTotal: showExactResultCount,
  });
  const canRequest = useCanRequest();
  const isQuerySource = state.source === "query" && Boolean(state.q);
  // Add a 200ms TMDB debounce on top of SearchBar's 200ms input debounce so the
  // TMDB plugin isn't hit at the same cadence as the local library query.
  const tmdbDebouncedQ = useDebounce(state.q ?? "", 200);
  const tmdbQuery = useRequestSearch("all", tmdbDebouncedQ, 1, {
    enabled: canRequest.discoveryEnabled && isQuerySource,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
  });
  const tmdbMissingCount =
    tmdbQuery.data?.results?.filter((result) => result.availability !== "available").length ?? 0;
  const libraryHasResults = (catalogQuery.data?.totalItems ?? 0) > 0;
  const libraryEmpty = !catalogQuery.isLoading && !libraryHasResults;
  // When the library is empty and the request section will (or might) render,
  // hide ItemGrid entirely. The previous approach pinned ItemGrid's `loading`
  // prop to true, which renders 24 skeleton tiles forever above the section.
  const tmdbMayRescueLibrary =
    isQuerySource &&
    libraryEmpty &&
    (canRequest.isResolving ||
      (canRequest.discoveryEnabled && (tmdbQuery.isLoading || tmdbMissingCount > 0)));
  const loadedHistoryItems = useMemo(() => {
    if (!isHistorySource) {
      return [] as BrowseItem[];
    }
    const seen = new Set<string>();
    const items: BrowseItem[] = [];
    (catalogQuery.data?.pages ?? new Map<number, BrowseItem[]>()).forEach((page) => {
      page.forEach((item) => {
        if (seen.has(item.content_id)) {
          return;
        }
        seen.add(item.content_id);
        items.push(item);
      });
    });
    return items;
  }, [catalogQuery.data?.pages, isHistorySource]);
  const selectedHistoryItems = useMemo(
    () => loadedHistoryItems.filter((item) => selectedIds.has(item.content_id)),
    [loadedHistoryItems, selectedIds],
  );
  const selectedHistoryTargets = useMemo(
    () => selectedHistoryItems.map((item) => buildHistoryRemovalTarget(item.content_id, item.type)),
    [selectedHistoryItems],
  );
  const title =
    catalogQuery.data?.title ?? state.title ?? defaultCatalogTitle(state.source, state.q);

  useDocumentTitle(title);

  useEffect(() => {
    setSelectionMode(false);
    setSelectedIds(new Set());
    setRemoveConfirmOpen(false);
  }, [searchKey, isHistorySource]);

  const toggleHistorySelection = useCallback((item: BrowseItem) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(item.content_id)) {
        next.delete(item.content_id);
      } else {
        next.add(item.content_id);
      }
      return next;
    });
  }, []);

  return (
    <div className="page-shell space-y-6 py-4 sm:py-6">
      <header className="page-header">
        <div className="space-y-3">
          <h1 className="page-title text-[clamp(2rem,5vw,3.5rem)]">{title}</h1>
          <p className="page-subtitle text-sm sm:text-base">
            Refine the archive by type, era, rating, or genre.
          </p>
        </div>
        <div className="items-baseline gap-3 sm:flex">
          <div className="hidden h-8 w-px bg-current opacity-15 sm:block" />
          {showExactResultCount ? (
            <div className="text-right tabular-nums">
              <span className="hidden text-3xl font-extralight tracking-tight sm:inline">
                {catalogQuery.data?.totalItems ?? 0}
              </span>
              <span className="text-muted-foreground ml-1.5 hidden text-xs font-medium tracking-widest uppercase sm:inline">
                result{(catalogQuery.data?.totalItems ?? 0) === 1 ? "" : "s"}
              </span>
              <span className="text-muted-foreground text-xs sm:hidden">
                {catalogQuery.data?.totalItems ?? 0} result
                {(catalogQuery.data?.totalItems ?? 0) === 1 ? "" : "s"}
              </span>
            </div>
          ) : null}
        </div>
      </header>

      {state.source === "query" ? (
        <div className="flex justify-center">
          <SearchBar prominent initialQuery={state.q ?? ""} autoFocus />
        </div>
      ) : null}

      <CatalogFiltersPanel
        state={state}
        onStateChange={(nextState) => {
          const nextSearchParams = buildCatalogApiSearchParams(nextState);
          if (nextSearchParams.toString() !== searchParams.toString()) {
            setSearchParams(nextSearchParams);
          }
        }}
      />

      {isHistorySource && (
        <section className="surface-panel flex flex-col gap-3 rounded-2xl border-0 p-4 sm:flex-row sm:items-center sm:justify-between">
          <div className="space-y-1">
            <p className="text-sm font-semibold">Watch History</p>
            <p className="text-muted-foreground text-xs sm:text-sm">
              Removing items clears watch history, watched status, and resume progress for this
              profile.
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            {!selectionMode ? (
              <Button variant="outline" size="sm" onClick={() => setSelectionMode(true)}>
                <CheckSquare className="size-4" />
                Select
              </Button>
            ) : (
              <>
                <span className="text-muted-foreground text-sm">{selectedIds.size} selected</span>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setSelectedIds(new Set(loadedHistoryItems.map((item) => item.content_id)))
                  }
                >
                  Select Loaded
                </Button>
                <Button variant="ghost" size="sm" onClick={() => setSelectedIds(new Set())}>
                  Clear
                </Button>
                <Button
                  variant="destructive"
                  size="sm"
                  disabled={selectedHistoryTargets.length === 0}
                  onClick={() => setRemoveConfirmOpen(true)}
                >
                  <Trash2 className="size-4" />
                  Remove Selected
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  onClick={() => {
                    setSelectionMode(false);
                    setSelectedIds(new Set());
                  }}
                >
                  <X className="size-4" />
                  Done
                </Button>
              </>
            )}
          </div>
        </section>
      )}

      {tmdbMayRescueLibrary ? null : (
        <ItemGrid
          totalItems={catalogQuery.data?.totalItems ?? 0}
          pages={catalogQuery.data?.pages ?? new Map()}
          pageSize={limit}
          loading={catalogQuery.isLoading}
          onVisibleRangeChange={handleVisibleRangeChange}
          selectionMode={isHistorySource && selectionMode}
          selectedIds={selectedIds}
          onToggleSelect={toggleHistorySelection}
        />
      )}

      {isQuerySource && canRequest.discoveryEnabled ? (
        <RequestToAddSection
          variant="grid"
          query={tmdbDebouncedQ}
          libraryHadHits={libraryHasResults}
        />
      ) : null}

      <ConfirmDialog
        open={removeConfirmOpen}
        onOpenChange={setRemoveConfirmOpen}
        title={historyRemovalDialogTitle(selectedHistoryTargets)}
        description={historyRemovalDialogDescription(selectedHistoryTargets)}
        confirmLabel="Remove"
        variant="destructive"
        isPending={removeHistory.isPending}
        onConfirm={() => {
          removeHistory.mutate(selectedHistoryTargets, {
            onSuccess: () => {
              setRemoveConfirmOpen(false);
              setSelectionMode(false);
              setSelectedIds(new Set());
            },
          });
        }}
      />
    </div>
  );
}
