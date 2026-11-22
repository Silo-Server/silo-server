import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { ArrowRight, RefreshCw } from "lucide-react";

import type { BrowseItem } from "@/api/types";
import CastCarousel from "@/components/CastCarousel";
import ItemCard from "@/components/ItemCard";
import ItemGrid from "@/components/ItemGrid";
import MediaCarousel from "@/components/MediaCarousel";
import RequestPosterCard from "@/components/RequestPosterCard";
import { RequestToAddSection } from "@/components/RequestToAddSection";
import SearchBar from "@/components/SearchBar";
import BoxSetSearchResults from "@/components/boxsets/BoxSetSearchResults";
import CatalogFiltersPanel from "@/components/catalog/CatalogFiltersPanel";
import SearchScopeChips from "@/components/catalog/SearchScopeChips";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useBoxSetSearch } from "@/hooks/queries/boxSets";
import { useCatalogWindow } from "@/hooks/queries/catalog";
import { usePersonSearch } from "@/hooks/queries/personSearch";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import { useCanRequest } from "@/hooks/useCanRequest";
import { useDebounce } from "@/hooks/useDebounce";
import { useCatalogVisibleRange } from "@/hooks/useCatalogVisibleRange";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useSearchMediaScope, useSearchScopeChips } from "@/hooks/useSearchMediaScope";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useWatchlistTitleToggle } from "@/hooks/useWatchlistTitleToggle";
import { numberText, useT } from "@/i18n";
import { querySortToSelectValue } from "@/lib/collectionSortConfig";
import { requestSearchTypeForScope, requestSuggestions } from "@/lib/mediaRequests";
import { getQuerySortOptions } from "@/lib/querySortOptions";
import { cardGridClasses } from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

import {
  buildCatalogFilterSearchParams,
  buildCatalogQueryUpdateHref,
  readCatalogRequestPage,
  searchSortsByRelevance,
  type CatalogSearchState,
} from "./catalogSearchParams";
import {
  readSearchView,
  splitOverviewResults,
  withRequestPage,
  withSearchView,
  type SearchResultsView,
} from "./searchResultsView";

const LIMIT = 60;
const REQUEST_SEARCH_DEBOUNCE_MS = 100;
const INTERACTIVE_SEARCH_GC_TIME_MS = 30_000;
const REQUEST_SHELF_LIMIT = 20;

/** Columns the poster grid renders at its current width. */
function useRenderedColumns(): [(el: HTMLDivElement | null) => void, number] {
  const [columns, setColumns] = useState(0);
  const observerRef = useRef<ResizeObserver | null>(null);
  const ref = useCallback((el: HTMLDivElement | null) => {
    observerRef.current?.disconnect();
    observerRef.current = null;
    if (!el) return;
    const measure = () => {
      const count = getComputedStyle(el).gridTemplateColumns.split(" ").filter(Boolean).length;
      setColumns((current) => (current === count ? current : count));
    };
    measure();
    if (typeof ResizeObserver !== "undefined") {
      observerRef.current = new ResizeObserver(measure);
      observerRef.current.observe(el);
    }
  }, []);
  useEffect(() => () => observerRef.current?.disconnect(), []);
  return [ref, columns];
}

function SectionHeader({ title, action, id }: { title: string; action?: ReactNode; id?: string }) {
  return (
    <div className="flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1">
      <h2 id={id} className="text-lg font-semibold tracking-tight sm:text-xl">
        {title}
      </h2>
      {action}
    </div>
  );
}

export default function SearchResults({
  searchParams,
  setSearchParams,
  state,
}: {
  searchParams: URLSearchParams;
  setSearchParams: (nextInit: URLSearchParams) => void;
  state: CatalogSearchState;
}) {
  const t = useT();
  const query = state.q ?? "";
  const view = readSearchView(searchParams);
  useDocumentTitle(t("web.catalog.page.resultsFor", { query }));

  // Scope: the URL `type` wins; without it the viewer's saved scope applies.
  const { scope: preferredScope } = useSearchMediaScope();
  const hasExplicitScope = searchParams.has("type");
  const effectiveState = useMemo(() => {
    if (hasExplicitScope || preferredScope === "all") return state;
    return {
      ...state,
      query_definition: { ...state.query_definition, media_scope: preferredScope },
    };
  }, [hasExplicitScope, preferredScope, state]);
  const mediaScope = effectiveState.query_definition.media_scope;
  const scopeChips = useSearchScopeChips(mediaScope, searchParams, setSearchParams);
  const buildSearchHref = useCallback(
    (q: string) => buildCatalogQueryUpdateHref(effectiveState, q),
    [effectiveState],
  );
  const showView = useCallback(
    (next: SearchResultsView) => {
      setSearchParams(withSearchView(searchParams, next));
      window.scrollTo({ top: 0 });
    },
    [searchParams, setSearchParams],
  );

  // Library results. The overview reads only the first page; the library view
  // windows through every page as the grid scrolls.
  const searchKey = withSearchView(searchParams, "overview").toString();
  const [libraryRange, handleVisibleRangeChange] = useCatalogVisibleRange(searchKey, LIMIT);
  const visibleRange: [number, number] = view === "library" ? libraryRange : [0, LIMIT - 1];
  const catalogQuery = useCatalogWindow(effectiveState, {
    limit: LIMIT,
    visibleRange,
    includeTotal: false,
  });
  const firstPage = catalogQuery.data?.pages.get(0) ?? [];
  const knownItems = catalogQuery.data?.knownItems ?? 0;
  const totalItems = catalogQuery.data?.totalItems ?? 0;
  const libraryResultsKnown =
    !catalogQuery.isLoading && !catalogQuery.isPlaceholderData && !catalogQuery.isError;
  const libraryEmpty = libraryResultsKnown && knownItems === 0;
  // Meilisearch reports no exact total, so past the loaded pages it is "60+".
  const countLabel = (count: number, more: boolean) =>
    more ? t("web.search.results.atLeastCount", { count: numberText(count) }) : numberText(count);
  const libraryCountLabel = countLabel(knownItems, totalItems > knownItems);

  // Requestable titles from TMDB, minus what the library already has.
  const canRequest = useCanRequest();
  const requestSearchType = requestSearchTypeForScope(mediaScope);
  const tmdbQ = useDebounce(query, REQUEST_SEARCH_DEBOUNCE_MS);
  const requestsEnabled = canRequest.discoveryEnabled && requestSearchType !== null;
  const requestQuery = useRequestSearch(requestSearchType ?? "all", tmdbQ, 1, {
    enabled: requestsEnabled,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
    gcTime: INTERACTIVE_SEARCH_GC_TIME_MS,
    retry: false,
  });
  const requestable = requestsEnabled
    ? requestSuggestions(requestQuery.data?.results, REQUEST_SHELF_LIMIT)
    : [];
  // A disabled query keeps its cached data, such as another scope's pages.
  const requestPages = requestsEnabled ? (requestQuery.data?.total_pages ?? 0) : 0;
  // A later page can hold titles the first page's library matches hid.
  const hasRequestResults = requestable.length > 0 || requestPages > 1;
  // The full request list needs discovery for this scope; an audiobook scope
  // or a search with nothing to request shows the overview instead.
  // While discovery has not answered (feature status loading, or the search
  // still waiting for a profile), a linked request view stays put.
  // A query shorter than two characters never searches TMDB (useRequestSearch).
  const requestsDeciding =
    canRequest.isResolving ||
    (requestsEnabled &&
      tmdbQ.trim().length > 1 &&
      requestQuery.data === undefined &&
      !requestQuery.isError);
  const shownView: SearchResultsView =
    view === "requests" && !requestsDeciding && !(requestsEnabled && hasRequestResults)
      ? "overview"
      : view;
  // Nothing in the library and, once discovery has answered, nothing to
  // request either.
  const requestsSettled =
    !canRequest.isResolving &&
    (!requestsEnabled || (!requestQuery.isLoading && !requestQuery.isFetching));
  const watchlist = useWatchlistTitleToggle();

  // People and box sets show only in the overview.
  const inOverview = shownView === "overview";
  const peopleQuery = usePersonSearch(query, 20, inOverview, mediaScope);
  const people = peopleQuery.data ?? [];
  const boxSetsInScope = !mediaScope || mediaScope === "movie" || mediaScope === "video";
  const boxSetQuery = useBoxSetSearch(query, {
    enabled: boxSetsInScope && inOverview,
    libraryId: state.library_id ? String(state.library_id) : undefined,
  });
  const boxSets = boxSetsInScope ? (boxSetQuery.data ?? []) : [];

  const [gridRef, columns] = useRenderedColumns();
  const { cardPresentation } = useUICustomization();
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const gridClasses = cardGridClasses(cardPresentation.poster_size);
  // While the next search loads, the last one's rows stay dimmed, as in ItemGrid.
  const overviewGridClasses = cn(
    gridClasses,
    "transition-opacity",
    catalogQuery.isPlaceholderData && "opacity-60",
  );
  const split = splitOverviewResults(
    firstPage,
    columns,
    catalogQuery.data?.firstPageComplete === false,
  );
  // "Best matches" only means something in relevance order. Under another sort
  // the same rows read as one list, with the request row two rows in.
  const byRelevance = searchSortsByRelevance(effectiveState);
  const sortName = byRelevance
    ? ""
    : (getQuerySortOptions(true).find(
        (option) => option.value === effectiveState.query_definition.sort.field,
      )?.label ?? "");
  const sortByRelevance = () => {
    const next = new URLSearchParams(searchParams);
    next.set("sort", "relevance");
    next.set("order", "desc");
    setSearchParams(next);
  };

  const viewTabs: Array<{ view: SearchResultsView; label: string; count?: string }> = [
    { view: "overview", label: t("web.search.results.overview") },
    {
      view: "library",
      label: t("web.search.results.inLibrary"),
      // A count before the library answers would be 0, or the last search's.
      count: libraryResultsKnown ? libraryCountLabel : undefined,
    },
  ];
  if (requestsEnabled && hasRequestResults) {
    viewTabs.push({
      view: "requests",
      label: t("web.search.results.requestToAdd"),
      count: requestable.length > 0 ? countLabel(requestable.length, requestPages > 1) : undefined,
    });
  }
  const openLibrary = () => showView("library");
  const showAllLink = split.hasMoreThanShown ? (
    <button
      type="button"
      onClick={openLibrary}
      className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1.5 text-sm font-semibold"
    >
      {t("web.search.results.showAll")}
      <ArrowRight className="size-4" aria-hidden />
    </button>
  ) : null;
  const showAllButton = split.hasMoreThanShown ? (
    <div className="flex justify-center pt-4">
      <Button variant="outline" className="rounded-full px-6" onClick={openLibrary}>
        {t("web.search.results.showAll")}
        <span className="text-muted-foreground tabular-nums">{libraryCountLabel}</span>
      </Button>
    </div>
  ) : null;

  const nothingFound =
    libraryEmpty &&
    requestsSettled &&
    !hasRequestResults &&
    boxSets.length === 0 &&
    people.length === 0 &&
    !peopleQuery.isLoading &&
    !peopleQuery.isError;

  const peopleContent = peopleQuery.isError ? (
    <div role="alert" className="space-y-2">
      <p className="text-muted-foreground text-sm">{t("web.catalog.page.peopleFailed")}</p>
      <Button variant="outline" size="sm" onClick={() => void peopleQuery.refetch()}>
        <RefreshCw className="size-4" />
        {t("web.catalog.page.retryPeople")}
      </Button>
    </div>
  ) : peopleQuery.isLoading && libraryEmpty ? (
    // With no library titles, people may be the whole answer; say they are coming.
    <p role="status" className="text-muted-foreground text-sm">
      {t("web.catalog.page.searchingPeople")}
    </p>
  ) : people.length > 0 ? (
    <CastCarousel
      cast={people.map((person, index) => ({
        person_id: person.id,
        name: person.name,
        photo_url: person.photo_url,
        character: "",
        order: index,
      }))}
    />
  ) : null;
  const peopleSection = peopleContent ? (
    <section aria-label={t("web.catalog.page.people")} className="space-y-3">
      <SectionHeader title={t("web.catalog.page.people")} />
      {peopleContent}
    </section>
  ) : null;

  const renderCards = (items: BrowseItem[]) =>
    items.map((item) => (
      <div key={item.content_id} role="listitem">
        <ItemCard item={item} overlayPrefs={overlayPrefs} quickActionMode={quickActionMode} />
      </div>
    ));

  const requestGrid =
    requestsEnabled && requestSearchType ? (
      <RequestToAddSection
        variant="grid"
        query={tmdbQ}
        mediaType={requestSearchType}
        libraryHadHits={!libraryEmpty}
        libraryResultsKnown={libraryResultsKnown}
        page={readCatalogRequestPage(searchParams)}
        onPageChange={(page) => setSearchParams(withRequestPage(searchParams, view, page))}
      />
    ) : null;

  const errorCard = (
    <div
      role="alert"
      className="search-paint-surface flex flex-col items-center gap-3 rounded-2xl border px-4 py-16 text-center"
    >
      <p className="font-medium">{t("web.catalog.page.searchFailed")}</p>
      <p className="text-muted-foreground max-w-md text-sm">
        {t("web.catalog.page.searchFailedBody")}
      </p>
      <Button variant="outline" size="sm" onClick={() => void catalogQuery.refetch()}>
        <RefreshCw className="size-4" />
        {t("web.catalog.page.retrySearch")}
      </Button>
    </div>
  );

  const skeletonGrid = (
    <div className={gridClasses} aria-busy>
      {Array.from({ length: 12 }).map((_, index) => (
        <div key={index}>
          <Skeleton className="aspect-[2/3] rounded-lg" />
          <Skeleton className="mt-2 h-4 w-3/4" />
        </div>
      ))}
    </div>
  );

  // The library's first rows: the error, a skeleton, or two rows of titles. A
  // failed search shows no titles, since what is cached is an earlier query's.
  const firstRows = catalogQuery.isError ? (
    errorCard
  ) : catalogQuery.isLoading ? (
    skeletonGrid
  ) : split.best.length > 0 ? (
    <section aria-labelledby="search-first" className="space-y-3">
      <SectionHeader
        id="search-first"
        title={t(byRelevance ? "web.search.results.bestMatches" : "web.search.results.inLibrary")}
        action={byRelevance ? undefined : showAllLink}
      />
      {byRelevance ? null : (
        <p className="text-muted-foreground -mt-2 flex flex-wrap gap-x-3 text-sm">
          <span>{t("web.search.results.sortedBy", { sort: sortName })}</span>
          <button
            type="button"
            onClick={sortByRelevance}
            className="hover:text-foreground underline underline-offset-4"
          >
            {t("web.search.results.backToRelevance")}
          </button>
        </p>
      )}
      <div ref={gridRef} role="list" className={overviewGridClasses}>
        {renderCards(split.best)}
      </div>
    </section>
  ) : null;

  // Without library titles (none, or the search failed) the request results
  // are the answer; otherwise they are one row between the library's rows.
  const requestRow =
    libraryEmpty || catalogQuery.isError ? (
      nothingFound ? (
        <p className="text-muted-foreground py-12 text-center">{t("web.catalog.grid.noItems")}</p>
      ) : (
        requestGrid
      )
    ) : requestable.length > 0 ? (
      <section className="bg-card/60 border-border/60 rounded-2xl border px-4 py-5 sm:px-5">
        <MediaCarousel
          title={t("web.search.results.notInLibrary")}
          onViewAll={() => showView("requests")}
          edgePadding={false}
        >
          {requestable.map((item) => (
            <RequestPosterCard
              key={`${item.media_type}-${item.tmdb_id}`}
              variant="discover"
              item={item}
              onToggleWatchlist={watchlist.enabled ? () => watchlist.toggle(item) : undefined}
              isWatchlistPending={watchlist.isPending(item)}
            />
          ))}
        </MediaCarousel>
      </section>
    ) : null;

  // Under relevance the rest are "More results"; under another sort they
  // continue the same list without a heading of their own.
  const moreRows = catalogQuery.isError ? null : split.more.length > 0 ? (
    // Under another sort these rows continue the first section's list, so they
    // don't get a second region with the same name.
    <section aria-labelledby={byRelevance ? "search-more" : undefined} className="space-y-3">
      {byRelevance ? (
        <SectionHeader
          id="search-more"
          title={t("web.search.results.moreResults")}
          action={showAllLink}
        />
      ) : null}
      <div role="list" className={overviewGridClasses}>
        {renderCards(split.more)}
      </div>
      {showAllButton}
    </section>
  ) : (
    showAllButton
  );

  let body: ReactNode;
  if (shownView === "requests") {
    // Until discovery answers, the grid has nothing to draw.
    body = requestsDeciding ? skeletonGrid : requestGrid;
  } else if (shownView === "library") {
    body = catalogQuery.isError ? (
      errorCard
    ) : (
      <section aria-label={t("web.search.results.allInLibrary")} className="space-y-4">
        <SectionHeader title={t("web.search.results.allInLibrary")} />
        <ItemGrid
          totalItems={totalItems}
          knownItems={catalogQuery.data?.knownItems}
          stale={catalogQuery.isPlaceholderData}
          pages={catalogQuery.data?.pages ?? new Map()}
          pageSize={LIMIT}
          loading={catalogQuery.isLoading}
          onVisibleRangeChange={handleVisibleRangeChange}
          posterAspect={mediaScope === "audiobook" ? "square" : "portrait"}
        />
      </section>
    );
  } else {
    body = (
      <>
        {boxSets.length > 0 ? <BoxSetSearchResults boxSets={boxSets} /> : null}
        {firstRows}
        {requestRow}
        {byRelevance ? peopleSection : null}
        {moreRows}
        {byRelevance ? null : peopleSection}
      </>
    );
  }

  return (
    <div className="space-y-8 px-4 py-4 sm:px-6 sm:py-6 lg:px-10 xl:px-12">
      <header className="space-y-4">
        {/* The search field reads as the page's heading; this names the page for assistive tech. */}
        <h1 className="sr-only">{t("web.catalog.page.resultsFor", { query })}</h1>
        <SearchBar hero initialQuery={query} autoFocus buildSearchHref={buildSearchHref} />
        <div className="flex flex-wrap items-center gap-3">
          <SearchScopeChips
            activeScope={scopeChips.activeScope}
            onScopeChange={scopeChips.onScopeChange}
          />
          <div className="w-full min-w-0 sm:w-auto sm:flex-1">
            <CatalogFiltersPanel
              relevanceSort
              allowPersonalizedFilters
              allowPersonalizedSorts
              state={effectiveState}
              onStateChange={(nextState) => {
                const sortChanged =
                  querySortToSelectValue(nextState.query_definition.sort) !==
                  querySortToSelectValue(effectiveState.query_definition.sort);
                const forNavigation = sortChanged
                  ? { ...nextState, sort_from_server: false, explicit_sort: true }
                  : nextState;
                const next = buildCatalogFilterSearchParams(forNavigation);
                if (shownView !== "overview") next.set("view", shownView);
                if (next.toString() !== searchParams.toString()) setSearchParams(next);
              }}
            />
          </div>
        </div>
        <nav
          aria-label={t("web.search.results.viewsLabel")}
          className="flex flex-wrap items-center gap-1"
        >
          {viewTabs.map((tab) => {
            const active = tab.view === shownView;
            return (
              <button
                key={tab.view}
                type="button"
                aria-pressed={active}
                // A request view that fell back to the overview still names
                // itself in the URL; pressing Overview clears it.
                onClick={() => tab.view !== view && showView(tab.view)}
                className={cn(
                  "inline-flex items-baseline gap-2 rounded-full px-4 py-2 text-sm font-semibold transition-colors",
                  active
                    ? "bg-accent text-foreground"
                    : "text-muted-foreground hover:text-foreground",
                )}
              >
                {tab.label}
                {tab.count ? (
                  <span className="text-muted-foreground text-xs font-medium tabular-nums">
                    {tab.count}
                  </span>
                ) : null}
              </button>
            );
          })}
        </nav>
      </header>

      {body}
    </div>
  );
}
