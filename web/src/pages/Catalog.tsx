import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import { CheckSquare, RefreshCw, Search, Trash2, X } from "lucide-react";

import { captureProfileRequestContext } from "@/api/client";
import type { BrowseItem } from "@/api/types";
import { isNotFoundProblem } from "@/api/v2/request";
import ItemGrid from "@/components/ItemGrid";
import { cn } from "@/lib/utils";
import { formatLocale } from "@/lib/appLocale";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import SearchScopeChips from "@/components/catalog/SearchScopeChips";
import { useCatalogWindow } from "@/hooks/queries/catalog";
import { useSetCollectionSortPreference } from "@/hooks/queries/collections";
import { querySortToSelectValue } from "@/lib/collectionSortConfig";
import { useSearchScopeChips } from "@/hooks/useSearchMediaScope";
import { useRemoveHistory } from "@/hooks/queries/history";
import { useStartShuffle } from "@/hooks/queries/shuffles";
import { useRequestFeatureStatus } from "@/hooks/queries/useRequests";
import { useWatchlistTitles } from "@/hooks/queries/watchlistTitles";
import WatchlistTabs, { WatchlistTabPanel } from "@/components/watchlist/WatchlistTabs";
import WatchlistTitlesTab from "@/components/watchlist/WatchlistTitlesTab";
import {
  parseWatchlistTab,
  WATCHLIST_NOT_IN_LIBRARY_TAB,
  watchlistTitleNeedsAttention,
  watchlistTitlesAvailable,
  type WatchlistTab,
} from "@/lib/watchlistTitles";
import { useCatalogVisibleRange } from "@/hooks/useCatalogVisibleRange";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { numberText, useT, type Translate } from "@/i18n";
import SearchBar from "@/components/SearchBar";
import SearchResults from "./SearchResults";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { ReadOnlyCollectionCallout } from "@/components/collections/CollectionPageActions";
import {
  useCollectionPageAccess,
  type CollectionPageTarget,
} from "@/hooks/useCollectionPageAccess";
import { useCollectionPageHeader } from "@/hooks/useCollectionPageHeader";
import { CollectionPageHero } from "@/components/collections/CollectionPageHero";
import {
  buildHistoryRemovalTarget,
  historyRemovalDialogDescription,
  historyRemovalDialogTitle,
} from "@/lib/historyRemoval";

import {
  buildCatalogFilterSearchParams,
  buildCatalogQueryUpdateHref,
  catalogSourceAllowsOverlay,
  parseCatalogSearchParams,
} from "./catalogSearchParams";
import type { CatalogSearchState } from "./catalogSearchParams";

function defaultCatalogTitle(t: Translate, source: string, searchQuery?: string) {
  if (source === "favorites") return t("web.catalog.page.favorites");
  if (source === "watchlist") return t("web.catalog.page.watchlist");
  if (source === "history") return t("web.catalog.page.history");
  if (searchQuery) return t("web.catalog.page.resultsFor", { query: searchQuery });
  return t("web.catalog.page.catalog");
}

function defaultCatalogSubtitle(t: Translate, source: string): string {
  if (source === "favorites") return t("web.catalog.page.favoritesSubtitle");
  if (source === "watchlist") return t("web.catalog.page.watchlistSubtitle");
  if (source === "history") return t("web.catalog.page.historySubtitle");
  return t("web.catalog.page.catalogSubtitle");
}

export default function Catalog() {
  const [searchParams, setSearchParams] = useSearchParams();
  const state = useMemo(() => parseCatalogSearchParams(searchParams), [searchParams]);
  // A query with filters (a studio, a genre) lists its matches without search text.
  const isEmptySearch =
    state.source === "query" &&
    !state.q &&
    !state.library_id &&
    state.query_definition.groups.length === 0;

  // Each view owns its tab title. A title set here too would run after the
  // results' own on mount and replace it whenever the results were cached.
  if (isEmptySearch) {
    return <EmptySearch />;
  }

  if (state.source === "query" && state.q) {
    return (
      <SearchResults searchParams={searchParams} setSearchParams={setSearchParams} state={state} />
    );
  }

  return (
    <CatalogResults searchParams={searchParams} setSearchParams={setSearchParams} state={state} />
  );
}

function EmptySearch() {
  const t = useT();
  useDocumentTitle(t("web.catalog.page.searchTitle"));

  return (
    <section className="page-shell flex min-h-[calc(100dvh-10rem)] flex-col items-center justify-center py-16 text-center">
      <div className="text-muted-foreground mb-6">
        <Search className="h-10 w-10" strokeWidth={1.5} />
      </div>
      <h1 className="page-title mb-4">{t("web.catalog.page.searchTitle")}</h1>
      <p className="page-subtitle mb-8 max-w-xl text-sm sm:text-base">
        {t("web.catalog.page.searchSubtitle")}
      </p>
      <SearchBar autoFocus prominent />
    </section>
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
  const t = useT();
  const limit = 60;
  const searchKey = searchParams.toString();
  const [visibleRange, handleVisibleRangeChange] = useCatalogVisibleRange(searchKey, limit);
  const isHistorySource = state.source === "history";
  const isCollectionSource =
    state.source === "library_collection" || state.source === "user_collection";
  const hasSavedSortPreference =
    isCollectionSource || state.source === "watchlist" || state.source === "favorites";
  const allowPersonalizedOverlayControls = catalogSourceAllowsOverlay(state.source);
  const removeHistory = useRemoveHistory();
  const { startShuffle, isStarting: isStartingShuffle } = useStartShuffle();
  const [selectionMode, setSelectionMode] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [removeConfirmOpen, setRemoveConfirmOpen] = useState(false);

  const buildSearchHref = useCallback(
    (query: string) => buildCatalogQueryUpdateHref(state, query),
    [state],
  );

  const mediaScope = state.query_definition.media_scope;
  // A text search renders SearchResults; this page lists a source or browses
  // a library, so it applies only the scope its URL names.
  const { activeScope: activeChipScope, onScopeChange: handleChipScopeChange } =
    useSearchScopeChips(mediaScope, searchParams, setSearchParams);

  // The watchlist splits into the library grid and the titles the library
  // doesn't have yet, once the server keeps watchlist entries for those.
  const isWatchlistSource = state.source === "watchlist";
  const requestFeatureStatus = useRequestFeatureStatus({ enabled: isWatchlistSource });
  const showWatchlistTabs =
    isWatchlistSource && watchlistTitlesAvailable(requestFeatureStatus.data);
  const watchlistTitles = useWatchlistTitles({ enabled: showWatchlistTabs });
  const watchlistTabsId = useId();
  const watchlistTab: WatchlistTab = showWatchlistTabs
    ? parseWatchlistTab(searchParams.get("tab"))
    : "library";
  const showingWatchlistTitles = watchlistTab === WATCHLIST_NOT_IN_LIBRARY_TAB;
  const watchlistNeedsAttention = useMemo(
    () => (watchlistTitles.data ?? []).some(watchlistTitleNeedsAttention),
    [watchlistTitles.data],
  );
  const setWatchlistTab = useCallback(
    (tab: WatchlistTab) => {
      const next = new URLSearchParams(searchParams);
      if (tab === WATCHLIST_NOT_IN_LIBRARY_TAB) {
        next.set("tab", tab);
      } else {
        next.delete("tab");
      }
      setSearchParams(next);
    },
    [searchParams, setSearchParams],
  );

  const showExactResultCount = state.source !== "section" && !showingWatchlistTitles;
  const catalogQuery = useCatalogWindow(state, {
    limit,
    visibleRange,
    includeTotal: showExactResultCount,
  });
  // The server resolves a collection or personal list's effective order when
  // the URL carries no sort.
  // Reflect that back into the filter bar so the menu shows what is actually
  // applied, without writing it into the URL — leaving it out of the URL is what
  // lets a later change to the saved preference take effect on the next visit.
  const effectiveSort = catalogQuery.data?.effectiveSort;
  const sortedState = useMemo(() => {
    if (!hasSavedSortPreference || !state.uses_source_order || !effectiveSort?.field) {
      return state;
    }
    return {
      ...state,
      uses_source_order: false,
      sort_from_server: true,
      query_definition: {
        ...state.query_definition,
        sort: { field: effectiveSort.field, order: effectiveSort.order },
      },
    };
  }, [effectiveSort, state, hasSavedSortPreference]);

  const saveCollectionSortPreference = useSetCollectionSortPreference();
  const rememberCollectionSort = useCallback(
    (nextState: CatalogSearchState) => {
      const collectionId = nextState.collection_id?.trim();
      let collectionKind: "library" | "user" | "watchlist" | "favorites";
      if (nextState.source === "library_collection") {
        if (!collectionId) return;
        collectionKind = "library";
      } else if (nextState.source === "user_collection") {
        if (!collectionId) return;
        collectionKind = "user";
      } else if (nextState.source === "watchlist" || nextState.source === "favorites") {
        collectionKind = nextState.source;
      } else {
        return;
      }
      const nextValue = nextState.uses_source_order
        ? ""
        : querySortToSelectValue(nextState.query_definition.sort);
      const currentValue = sortedState.uses_source_order
        ? ""
        : querySortToSelectValue(sortedState.query_definition.sort);
      if (nextValue === currentValue) return;
      // Captured here, at the moment of the pick, rather than inside the
      // mutation: these writes are serialized, so one that runs later must
      // still land on the profile that actually chose the sort.
      const profileAuth = captureProfileRequestContext();
      if (!profileAuth) return;
      const [field, order] = nextValue ? nextValue.split(":") : ["", ""];
      void saveCollectionSortPreference({
        collection_kind: collectionKind,
        collection_id: collectionId,
        field: field ?? "",
        order: order === "asc" || order === "desc" ? order : "",
        profileAuth,
      });
    },
    [saveCollectionSortPreference, sortedState],
  );

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
  // A collection the viewer cannot reach answers 404, the same for a deleted
  // collection and one they were never shown, so the page says neither.
  const collectionUnavailable = isCollectionSource && isNotFoundProblem(catalogQuery.sourceError);
  const collectionTarget: CollectionPageTarget | null =
    isCollectionSource && state.collection_id
      ? {
          scope: state.source === "library_collection" ? "server" : "personal",
          id: state.collection_id,
          libraryId: state.library_id,
        }
      : null;
  const collectionAccess = useCollectionPageAccess(collectionTarget);
  const collectionHeader = useCollectionPageHeader(collectionUnavailable ? null : collectionTarget);
  const title = collectionUnavailable
    ? t("web.catalog.page.notFound")
    : (catalogQuery.data?.title ?? state.title ?? defaultCatalogTitle(t, state.source, state.q));

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

  const totalItems = catalogQuery.data?.totalItems ?? 0;
  const shuffleCollectionId = isCollectionSource ? state.collection_id?.trim() : undefined;
  const shuffleKind = state.source === "user_collection" ? "user_collection" : "library_collection";
  const collectionShuffle =
    shuffleCollectionId && totalItems > 0
      ? {
          starting: isStartingShuffle,
          onShuffle: () => startShuffle({ kind: shuffleKind, id: shuffleCollectionId }),
        }
      : undefined;
  // A library browse (a query source without search text) counts titles in
  // the person's libraries, so the label says "in library".
  const inLibrary = state.source === "query";
  const resultNoun = inLibrary
    ? t("web.catalog.page.inLibraryNoun")
    : t("web.catalog.page.resultNoun", { count: totalItems });
  const resultCount = inLibrary
    ? t("web.catalog.page.inLibraryCount", { count: totalItems })
    : t("web.catalog.page.resultCount", { count: totalItems, shown: totalItems });

  if (collectionUnavailable) {
    return (
      <PageUnavailable
        title={t("web.catalog.page.collectionUnavailable")}
        description={t("web.catalog.page.collectionUnavailableBody")}
      >
        <Button asChild variant="outline">
          <ViewTransitionLink to="/collections" up>
            {t("web.catalog.page.allCollections")}
          </ViewTransitionLink>
        </Button>
      </PageUnavailable>
    );
  }

  return (
    // A collection uses the whole width, like the library view and the Collections wall.
    <div className={cn(!isCollectionSource && "page-shell", "space-y-6 py-4 sm:py-6")}>
      {searchParams.get("notice") === "read-only" ? (
        <ReadOnlyCollectionCallout access={collectionAccess} collectionName={title} />
      ) : null}
      {isCollectionSource ? (
        <CollectionPageHero
          title={title}
          access={collectionAccess}
          header={collectionHeader}
          libraryId={state.library_id}
          shuffle={collectionShuffle}
        />
      ) : (
        <header className="page-header">
          <div className="space-y-3">
            <h1 className="page-title text-[clamp(2rem,5vw,3.5rem)]">{title}</h1>
            <p className="page-subtitle text-sm sm:text-base">
              {defaultCatalogSubtitle(t, state.source)}
            </p>
          </div>
          <div className="flex flex-col items-start gap-3 sm:items-end">
            <div className="items-baseline gap-3 sm:flex">
              <div className="hidden h-8 w-px bg-current opacity-15 sm:block" />
              {showExactResultCount ? (
                <div className="text-end tabular-nums" role="status" aria-live="polite">
                  <span className="hidden text-3xl font-extralight tracking-tight sm:inline">
                    {numberText(totalItems)}
                  </span>
                  <span className="text-muted-foreground ms-1.5 hidden text-xs font-medium tracking-widest uppercase sm:inline">
                    {resultNoun}
                  </span>
                  <span className="text-muted-foreground text-xs sm:hidden">{resultCount}</span>
                </div>
              ) : null}
            </div>
          </div>
        </header>
      )}

      {showWatchlistTabs ? (
        <WatchlistTabs
          idBase={watchlistTabsId}
          value={watchlistTab}
          onValueChange={setWatchlistTab}
          count={watchlistTitles.data?.length}
          attention={watchlistNeedsAttention}
        />
      ) : null}

      {showingWatchlistTitles ? (
        <WatchlistTabPanel idBase={watchlistTabsId} value={WATCHLIST_NOT_IN_LIBRARY_TAB}>
          <WatchlistTitlesTab
            titles={watchlistTitles.data}
            isLoading={watchlistTitles.isLoading}
            isError={watchlistTitles.isError}
            onRetry={() => void watchlistTitles.refetch()}
            watchlistRequests={requestFeatureStatus.data?.watchlist_requests === true}
          />
        </WatchlistTabPanel>
      ) : (
        <WatchlistTabPanel idBase={watchlistTabsId} value="library" tabs={showWatchlistTabs}>
          {state.source === "query" ? (
            <div className="flex flex-col items-center gap-3">
              <SearchBar prominent autoFocus buildSearchHref={buildSearchHref} />
              <SearchScopeChips
                activeScope={activeChipScope}
                onScopeChange={handleChipScopeChange}
              />
            </div>
          ) : null}

          <CatalogFiltersPanel
            state={sortedState}
            // A collection's hero has no count of its own; the filter bar shows it.
            resultCountLabel={
              isCollectionSource && showExactResultCount
                ? t("web.catalog.page.titleCount", {
                    count: totalItems,
                    shown: totalItems.toLocaleString(formatLocale()),
                  })
                : undefined
            }
            resultCountLoading={
              isCollectionSource && (catalogQuery.isLoading || catalogQuery.isPlaceholderData)
            }
            onStateChange={(nextState) => {
              const sortChanged =
                nextState.uses_source_order !== sortedState.uses_source_order ||
                querySortToSelectValue(nextState.query_definition.sort) !==
                  querySortToSelectValue(sortedState.query_definition.sort);
              const stateForNavigation = sortChanged
                ? { ...nextState, sort_from_server: false, explicit_sort: true }
                : nextState;
              rememberCollectionSort(stateForNavigation);
              const nextSearchParams = buildCatalogFilterSearchParams(stateForNavigation);
              if (nextSearchParams.toString() !== searchParams.toString()) {
                setSearchParams(nextSearchParams);
              }
            }}
            allowLibrarySelection={!isCollectionSource}
            allowPersonalizedFilters={allowPersonalizedOverlayControls}
            allowPersonalizedSorts={
              isHistorySource
                ? "date_viewed"
                : state.source === "favorites" || state.source === "watchlist"
                  ? false
                  : allowPersonalizedOverlayControls
            }
          />

          {isHistorySource && (
            <section className="surface-panel flex flex-col gap-3 rounded-2xl border-0 p-4 sm:flex-row sm:items-center sm:justify-between">
              <div className="space-y-1">
                <p className="text-sm font-semibold">{t("web.catalog.page.historyHeading")}</p>
                <p className="text-muted-foreground text-xs sm:text-sm">
                  {t("web.catalog.page.historyBody")}
                </p>
              </div>
              <div className="flex flex-wrap items-center gap-2">
                {!selectionMode ? (
                  <Button variant="outline" size="sm" onClick={() => setSelectionMode(true)}>
                    <CheckSquare className="size-4" />
                    {t("web.catalog.page.select")}
                  </Button>
                ) : (
                  <>
                    <span className="text-muted-foreground text-sm">
                      {t("web.catalog.page.selectedCount", { count: selectedIds.size })}
                    </span>
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() =>
                        setSelectedIds(new Set(loadedHistoryItems.map((item) => item.content_id)))
                      }
                    >
                      {t("web.catalog.page.selectLoaded")}
                    </Button>
                    <Button variant="ghost" size="sm" onClick={() => setSelectedIds(new Set())}>
                      {t("web.catalog.page.clearSelection")}
                    </Button>
                    <Button
                      variant="destructive"
                      size="sm"
                      disabled={selectedHistoryTargets.length === 0}
                      onClick={() => setRemoveConfirmOpen(true)}
                    >
                      <Trash2 className="size-4" />
                      {t("web.catalog.page.removeSelected")}
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
                      {t("web.catalog.page.doneSelecting")}
                    </Button>
                  </>
                )}
              </div>
            </section>
          )}

          {catalogQuery.isError ? (
            <div
              className="search-paint-surface flex flex-col items-center justify-center gap-3 rounded-2xl border px-4 py-16 text-center"
              role="alert"
            >
              <p className="font-medium">{t("web.catalog.page.catalogFailed")}</p>
              <p className="text-muted-foreground max-w-md text-sm">
                {t("web.catalog.page.catalogFailedBody")}
              </p>
              <Button variant="outline" size="sm" onClick={() => void catalogQuery.refetch()}>
                <RefreshCw className="size-4" />
                {t("web.catalog.page.retryCatalog")}
              </Button>
            </div>
          ) : (
            <ItemGrid
              totalItems={catalogQuery.data?.totalItems ?? 0}
              knownItems={catalogQuery.data?.knownItems}
              stale={catalogQuery.isPlaceholderData}
              pages={catalogQuery.data?.pages ?? new Map()}
              pageSize={limit}
              loading={catalogQuery.isLoading}
              onVisibleRangeChange={handleVisibleRangeChange}
              narrowPosterActions={state.source === "favorites" || state.source === "watchlist"}
              selectionMode={isHistorySource && selectionMode}
              selectedIds={selectedIds}
              onToggleSelect={toggleHistorySelection}
              posterAspect={mediaScope === "audiobook" ? "square" : "portrait"}
            />
          )}
        </WatchlistTabPanel>
      )}

      <ConfirmDialog
        open={removeConfirmOpen}
        onOpenChange={setRemoveConfirmOpen}
        title={historyRemovalDialogTitle(selectedHistoryTargets)}
        description={historyRemovalDialogDescription(selectedHistoryTargets)}
        confirmLabel={t("web.catalog.page.removeConfirm")}
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
