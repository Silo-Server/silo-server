import { useMemo, useState, useEffect, useCallback, useRef } from "react";
import { useImageLoaded } from "@/hooks/useImageLoaded";
import { useQuery } from "@tanstack/react-query";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { VisuallyHidden } from "radix-ui";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { buildPersonCatalogHref, buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import { prefetchCatalog } from "@/pages/catalogRoute";
import { useSidebarItemNavigation } from "@/components/sidebarItemNavigationContext";
import { createEmptyQueryDefinition, type BrowseItem, type Person } from "@/api/types";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { usePersonSearch } from "@/hooks/queries/personSearch";
import { useSearchMediaScope } from "@/hooks/useSearchMediaScope";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import { useCanRequest } from "@/hooks/useCanRequest";
import { catalogKeys } from "@/hooks/queries/keys";
import { decodeThumbhash } from "@/lib/thumbhash";
import { getInitials } from "@/lib/text";
import { cn } from "@/lib/utils";
import { Search } from "lucide-react";
import { RequestToAddSection } from "./RequestToAddSection";
import CardPlayOverlay from "./CardPlayOverlay";

const PREVIEW_LIMIT = 8;
const PEOPLE_PREVIEW_LIMIT = 4;
const DEBOUNCE_MS = 200;
const TMDB_DEBOUNCE_MS = 400;
const INTERACTIVE_SEARCH_GC_TIME_MS = 30_000;

function typeLabel(type: BrowseItem["type"]): string {
  switch (type) {
    case "movie":
      return "Movie";
    case "series":
      return "Series";
    case "season":
      return "Season";
    case "episode":
      return "Episode";
    case "ebook":
      return "Ebook";
    case "audiobook":
      return "Audiobook";
    case "manga":
      return "Manga";
    default:
      return type;
  }
}

// Shared by the option row and the play-overlay layer stacked on top of it, so
// the overlay always lands on the poster even if the row's spacing changes.
const ROW_LAYOUT_CLASSES = "flex w-full cursor-pointer items-center gap-3 px-3 py-2 text-left";
const ROW_POSTER_CLASSES = "relative h-14 w-10 shrink-0";

function GlobalSearchResultRow({
  item,
  index,
  isSelected,
  onPick,
  onPlay,
}: {
  item: BrowseItem;
  index: number;
  isSelected: boolean;
  onPick: (contentId: string) => void;
  onPlay: () => void;
}) {
  const { loaded, onLoad, onError } = useImageLoaded(item.poster_url);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";

  // Virtual focus: keyboard focus stays in the search input and the option is
  // pointed at by aria-activedescendant, so the row is not a tab stop.
  //
  // The play link is a SIBLING of the option, not a child. role="option" is
  // "Children Presentational: True", so a link inside it would be stripped of
  // its role and name in the accessibility tree and become undiscoverable to
  // screen-reader users. The overlay layer instead repeats the row's own flex
  // layout (same gap/padding/poster box) so it tracks the poster without
  // hard-coded offsets, and is pointer-events-none so row clicks pass through.
  return (
    <div className="group/media hover:bg-muted/80 data-[selected]:bg-accent relative rounded-md transition-colors">
      <div
        id={`search-result-${index}`}
        role="option"
        aria-selected={isSelected}
        aria-label={[item.title, item.year > 0 ? String(item.year) : null, typeLabel(item.type)]
          .filter(Boolean)
          .join(", ")}
        data-selected={isSelected || undefined}
        onClick={() => onPick(item.content_id)}
        className={ROW_LAYOUT_CLASSES}
      >
        <div
          className={`bg-muted overflow-hidden rounded-md ${ROW_POSTER_CLASSES}`}
          style={
            thumbhashUrl
              ? {
                  backgroundImage: `url(${thumbhashUrl})`,
                  backgroundSize: "cover",
                  backgroundPosition: "center",
                }
              : undefined
          }
        >
          {item.poster_url ? (
            <img
              src={item.poster_url}
              alt=""
              className={`h-full w-full object-cover ${loaded ? "opacity-100" : "opacity-0"}`}
              loading="lazy"
              onLoad={onLoad}
              onError={onError}
            />
          ) : (
            <div className="text-muted-foreground flex h-full items-center justify-center px-1 text-center text-[10px] leading-tight">
              {item.title.slice(0, 24)}
            </div>
          )}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-sm font-medium">{item.title}</div>
          <div className="text-muted-foreground text-xs">
            {item.year > 0 ? `${item.year} · ` : ""}
            {typeLabel(item.type)}
          </div>
        </div>
      </div>
      {item.play_content_id ? (
        <div className={`pointer-events-none absolute inset-0 ${ROW_LAYOUT_CLASSES}`}>
          <div className={ROW_POSTER_CLASSES}>
            <CardPlayOverlay
              contentId={item.play_content_id}
              title={item.title}
              type={item.type === "movie" ? "movie" : "episode"}
              size="compact"
              onPlaybackStart={onPlay}
            />
          </div>
        </div>
      ) : null}
    </div>
  );
}

// Matches the "Request to Add" heading below the library results.
function ResultGroupHeading({ id, children }: { id: string; children: string }) {
  return (
    <div
      id={id}
      role="presentation"
      className="text-muted-foreground px-3 pt-2 pb-1 text-[10px] font-medium tracking-[0.1em] uppercase"
    >
      {children}
    </div>
  );
}

function GlobalSearchPersonRow({
  person,
  index,
  isSelected,
  onPick,
}: {
  person: Person;
  index: number;
  isSelected: boolean;
  onPick: (personId: string) => void;
}) {
  const { loaded, onLoad, onError } = useImageLoaded(person.photo_url);
  const thumbhashUrl = person.photo_thumbhash ? decodeThumbhash(person.photo_thumbhash) : "";

  return (
    <div
      id={`search-result-${index}`}
      role="option"
      aria-selected={isSelected}
      aria-label={`${person.name}, Person`}
      data-selected={isSelected || undefined}
      onClick={() => onPick(person.id)}
      className={cn(
        ROW_LAYOUT_CLASSES,
        "hover:bg-muted/80 data-[selected]:bg-accent rounded-md transition-colors",
      )}
    >
      <div className="flex w-10 shrink-0 justify-center">
        <div
          className="bg-muted text-muted-foreground relative flex h-10 w-10 items-center justify-center overflow-hidden rounded-full text-xs font-semibold"
          style={
            thumbhashUrl
              ? {
                  backgroundImage: `url(${thumbhashUrl})`,
                  backgroundSize: "cover",
                  backgroundPosition: "center",
                }
              : undefined
          }
        >
          {person.photo_url ? (
            <img
              src={person.photo_url}
              alt=""
              className={`absolute inset-0 h-full w-full object-cover ${loaded ? "opacity-100" : "opacity-0"}`}
              loading="lazy"
              onLoad={onLoad}
              onError={onError}
            />
          ) : (
            getInitials(person.name)
          )}
        </div>
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{person.name}</div>
        <div className="text-muted-foreground text-xs">Person</div>
      </div>
    </div>
  );
}

export function GlobalSearch({
  defaultOpen = false,
  initialQuery = "",
}: { defaultOpen?: boolean; initialQuery?: string } = {}) {
  const [open, setOpen] = useState(defaultOpen);
  const [query, setQuery] = useState(initialQuery);
  // Selection follows a result, not a position, so a row stays selected when
  // people results arrive and reorder the list.
  const [selectedKey, setSelectedKey] = useState<string | null>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);
  const navigate = useViewTransitionNavigate();
  const beginSidebarItemNavigation = useSidebarItemNavigation();
  const debouncedQuery = useDebounce(query.trim(), DEBOUNCE_MS);
  const tmdbDebouncedQuery = useDebounce(query.trim(), TMDB_DEBOUNCE_MS);
  const canRequest = useCanRequest();
  const tmdbQuery = useRequestSearch("all", tmdbDebouncedQuery, 1, {
    enabled: canRequest.discoveryEnabled,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
    gcTime: INTERACTIVE_SEARCH_GC_TIME_MS,
    retry: false,
  });
  const tmdbMissingCount =
    tmdbQuery.data?.results?.filter((result) => result.availability !== "available").length ?? 0;
  // Cap at DIALOG_LIMIT (4) — RequestToAddSection slices results to that many rows.
  const tmdbVisibleCount = Math.min(tmdbMissingCount, 4);
  const tmdbStillLoading =
    canRequest.discoveryEnabled && tmdbDebouncedQuery.length > 1 && tmdbQuery.isLoading;
  const tmdbWillRender = canRequest.discoveryEnabled && tmdbMissingCount > 0;
  // Hide empty state while the TMDB debounce trails the library debounce; otherwise
  // the user sees "No matches" flash between t=200ms and t=400ms after typing.
  const tmdbDebounceCatchingUp =
    canRequest.discoveryEnabled && tmdbDebouncedQuery !== debouncedQuery;

  // Preview results follow the user's preferred search scope (Media vs
  // Audiobooks vs All); the full results page applies the same default.
  const { scope: searchScope } = useSearchMediaScope();
  const searchState = useMemo(
    () =>
      createCatalogSearchState("query", {
        q: debouncedQuery || undefined,
        query_definition: {
          ...createEmptyQueryDefinition(),
          media_scope: searchScope === "all" ? undefined : searchScope,
        },
      }),
    [debouncedQuery, searchScope],
  );

  const previewQuery = useQuery({
    queryKey: catalogKeys.list({
      source: searchState.source,
      q: searchState.q,
      title: searchState.title,
      scope: searchState.scope,
      section_id: searchState.section_id,
      library_id: searchState.library_id,
      collection_id: searchState.collection_id,
      person_id: searchState.person_id,
      query_fingerprint: JSON.stringify(searchState.query_definition),
      include_total: false,
      limit: PREVIEW_LIMIT,
      offset: 0,
    }),
    queryFn: ({ signal }) => fetchCatalogPage(searchState, PREVIEW_LIMIT, 0, { signal }, false),
    enabled: open && debouncedQuery.length > 0,
    staleTime: 60 * 1000,
    gcTime: INTERACTIVE_SEARCH_GC_TIME_MS,
    retry: false,
  });
  // People follow the same scope as the titles. The hook returns nothing from
  // a server that cannot scope people search or filter it by viewer access.
  const peopleQuery = usePersonSearch(
    debouncedQuery,
    PEOPLE_PREVIEW_LIMIT,
    open && debouncedQuery.length > 0,
    searchScope === "all" ? undefined : searchScope,
  );

  useEffect(() => {
    function handleKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        setOpen((prev) => !prev);
      }
    }
    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, []);

  const handleSubmit = useCallback(
    (e: React.FormEvent) => {
      e.preventDefault();
      if (query.trim()) {
        navigate(buildQueryCatalogHref(query.trim()));
        setOpen(false);
        setQuery("");
      }
    },
    [navigate, query],
  );

  const handlePickItem = useCallback(
    (contentId: string) => {
      const href = `/item/${encodeURIComponent(contentId)}`;
      if (!beginSidebarItemNavigation?.({ href })) navigate(href);
      setOpen(false);
      setQuery("");
    },
    [beginSidebarItemNavigation, navigate],
  );

  const handlePickPerson = useCallback(
    (personId: string) => {
      navigate(buildPersonCatalogHref(personId));
      setOpen(false);
      setQuery("");
    },
    [navigate],
  );

  // Reset the selection when the query changes
  useEffect(() => {
    setSelectedKey(null);
  }, [query]);

  const showResultsPanel = query.trim().length > 0;
  const items = previewQuery.data?.items ?? [];
  // A failed people read only hides the group; the full search page retries it.
  const matchedPeople = peopleQuery.data ?? [];
  // The server ranks exact names first. Exact person matches are the best
  // results, so they lead on their own; otherwise people follow the titles.
  const exactPeople = matchedPeople.filter(
    (person) => person.name.trim().toLocaleLowerCase() === debouncedQuery.toLocaleLowerCase(),
  );
  const peopleFirst = exactPeople.length > 0;
  const people = peopleFirst ? exactPeople : matchedPeople;
  const itemOffset = peopleFirst ? people.length : 0;
  const peopleOffset = peopleFirst ? 0 : items.length;
  const resultKeys = [
    ...items.map((item) => `item:${item.content_id}`),
    ...people.map((person) => `person:${person.id}`),
  ];
  if (peopleFirst) resultKeys.unshift(...resultKeys.splice(items.length));
  const resultCount = resultKeys.length;
  const selectedIndex = selectedKey === null ? -1 : resultKeys.indexOf(selectedKey);

  // Auto-scroll the selected result into view. DOM focus deliberately stays in
  // the input; aria-activedescendant carries the selection.
  useEffect(() => {
    if (selectedIndex >= 0) {
      document.getElementById(`search-result-${selectedIndex}`)?.scrollIntoView?.({
        block: "nearest",
      });
    }
  }, [selectedIndex]);
  const hasMore = previewQuery.data?.has_more ?? false;
  const showLoading = (previewQuery.isFetching || peopleQuery.isFetching) && resultCount === 0;
  const showEmpty =
    !previewQuery.isFetching &&
    !peopleQuery.isFetching &&
    debouncedQuery.length > 0 &&
    resultCount === 0 &&
    !previewQuery.isError &&
    !peopleQuery.isError &&
    !tmdbStillLoading &&
    !tmdbWillRender &&
    !canRequest.isResolving &&
    !tmdbDebounceCatchingUp;
  // With nothing else to show, a failed people search cannot claim "No matches".
  const showError = previewQuery.isError || (peopleQuery.isError && resultCount === 0);
  function moveResultFocus(nextIndex: number) {
    if (resultCount === 0) {
      setSelectedKey(null);
      searchInputRef.current?.focus();
      return;
    }
    setSelectedKey(resultKeys[((nextIndex % resultCount) + resultCount) % resultCount]!);
  }
  function pickSelected() {
    const item = items[selectedIndex - itemOffset];
    if (item) {
      handlePickItem(item.content_id);
      return true;
    }
    const person = people[selectedIndex - peopleOffset];
    if (person) {
      handlePickPerson(person.id);
      return true;
    }
    return false;
  }
  const titleRows = items.map((item, i) => (
    <GlobalSearchResultRow
      key={item.content_id}
      item={item}
      index={itemOffset + i}
      isSelected={itemOffset + i === selectedIndex}
      onPick={handlePickItem}
      onPlay={() => setOpen(false)}
    />
  ));
  // With both kinds of result, each gets a heading and the lower one a
  // divider, so neither reads as part of the other.
  const grouped = people.length > 0 && items.length > 0;
  const peopleGroup =
    people.length > 0 ? (
      <div
        role="group"
        aria-labelledby="global-search-people-heading"
        className={cn(grouped && !peopleFirst && "mt-1 border-t border-white/5 pt-1")}
      >
        <ResultGroupHeading id="global-search-people-heading">People</ResultGroupHeading>
        {people.map((person, i) => (
          <GlobalSearchPersonRow
            key={person.id}
            person={person}
            index={peopleOffset + i}
            isSelected={peopleOffset + i === selectedIndex}
            onPick={handlePickPerson}
          />
        ))}
      </div>
    ) : null;
  const titlesGroup = grouped ? (
    <div
      role="group"
      aria-labelledby="global-search-titles-heading"
      className={cn(peopleFirst && "mt-1 border-t border-white/5 pt-1")}
    >
      <ResultGroupHeading id="global-search-titles-heading">Titles</ResultGroupHeading>
      {titleRows}
    </div>
  ) : (
    titleRows
  );

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v);
        if (!v) setQuery("");
      }}
    >
      <DialogContent
        className="top-[20%] max-h-[min(32rem,calc(100dvh-6rem))] translate-y-0 gap-0 overflow-hidden p-0 sm:max-w-lg"
        showCloseButton={false}
      >
        <VisuallyHidden.Root>
          <DialogTitle>Search</DialogTitle>
        </VisuallyHidden.Root>
        <form onSubmit={handleSubmit}>
          <div className={cn("flex items-center px-5 sm:px-6", showResultsPanel && "border-b")}>
            <Search className="text-muted-foreground mr-2 h-4 w-4 shrink-0" />
            <input
              ref={searchInputRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search library..."
              className="placeholder:text-muted-foreground flex h-12 w-full bg-transparent text-sm outline-none"
              autoFocus
              // Submitting opens the Catalog page, so its chunk starts loading
              // as soon as the search box takes focus.
              onFocus={prefetchCatalog}
              aria-label="Search"
              role="combobox"
              aria-expanded={showResultsPanel}
              aria-autocomplete="list"
              aria-controls="global-search-library-results"
              aria-activedescendant={
                selectedIndex >= 0 ? `search-result-${selectedIndex}` : undefined
              }
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  moveResultFocus(selectedIndex + 1);
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  moveResultFocus(selectedIndex < 0 ? resultCount - 1 : selectedIndex - 1);
                } else if (e.key === "Enter" && pickSelected()) {
                  e.preventDefault();
                } else if (e.key === "Escape") {
                  setOpen(false);
                }
              }}
            />
            <kbd className="bg-muted text-muted-foreground pointer-events-none ml-2 hidden rounded border px-1.5 py-0.5 text-[10px] font-medium select-none sm:inline-flex">
              ESC
            </kbd>
          </div>
        </form>
        {showResultsPanel && (
          <div className="flex min-h-0 flex-1 flex-col">
            <div className="max-h-[min(22rem,55vh)] overflow-y-auto overscroll-contain px-2 py-2">
              <div
                id="global-search-library-results"
                role="listbox"
                aria-label="Library search results"
              >
                {showLoading && (
                  <div className="text-muted-foreground px-3 py-6 text-center text-sm">
                    Searching...
                  </div>
                )}
                {showError && (
                  <div className="text-destructive px-3 py-4 text-center text-sm">
                    Could not load results. Press Enter to open the search page.
                  </div>
                )}
                {showEmpty && (
                  <div className="text-muted-foreground px-3 py-6 text-center text-sm">
                    No matches
                  </div>
                )}
                {peopleFirst && peopleGroup}
                {titlesGroup}
                {!peopleFirst && peopleGroup}
              </div>
              {tmdbDebouncedQuery.length > 1 && canRequest.discoveryEnabled && (
                <RequestToAddSection
                  variant="dialog"
                  query={tmdbDebouncedQuery}
                  libraryHadHits={items.length > 0}
                  libraryResultsKnown={!previewQuery.isFetching && !previewQuery.isError}
                />
              )}
            </div>
            <div role="status" aria-live="polite" className="sr-only">
              {tmdbVisibleCount > 0
                ? `${resultCount} library results, ${tmdbVisibleCount} request suggestions`
                : `${resultCount} results found`}
            </div>
            <div className="text-muted-foreground border-t px-3 py-2 text-center text-xs">
              {hasMore ? (
                <p>Showing top results. Press Enter for all results.</p>
              ) : (
                <p>Press Enter to open the full search page.</p>
              )}
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}
