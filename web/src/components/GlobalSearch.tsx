import { useMemo, useState, useEffect, useCallback } from "react";
import { useQuery } from "@tanstack/react-query";
import { Dialog, DialogContent, DialogTitle } from "@/components/ui/dialog";
import { VisuallyHidden } from "radix-ui";
import { useViewTransitionNavigate } from "@/hooks/useViewTransition";
import { useDebounce } from "@/hooks/useDebounce";
import { buildQueryCatalogHref } from "@/pages/catalogSearchParams";
import type { BrowseItem } from "@/api/types";
import { createCatalogSearchState, fetchCatalogPage } from "@/hooks/queries/catalog";
import { useRequestSearch } from "@/hooks/queries/useRequests";
import { useCanRequest } from "@/hooks/useCanRequest";
import { catalogKeys } from "@/hooks/queries/keys";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";
import { Search } from "lucide-react";
import { RequestToAddSection } from "./RequestToAddSection";

const PREVIEW_LIMIT = 8;
const DEBOUNCE_MS = 200;
const TMDB_DEBOUNCE_MS = 400;

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
    default:
      return type;
  }
}

function GlobalSearchResultRow({
  item,
  index,
  isSelected,
  onPick,
}: {
  item: BrowseItem;
  index: number;
  isSelected: boolean;
  onPick: (contentId: string) => void;
}) {
  const [loaded, setLoaded] = useState(false);
  const thumbhashUrl = item.poster_thumbhash ? decodeThumbhash(item.poster_thumbhash) : "";

  return (
    <button
      type="button"
      id={`search-result-${index}`}
      role="option"
      aria-selected={isSelected}
      data-selected={isSelected || undefined}
      onClick={() => onPick(item.content_id)}
      className="hover:bg-muted/80 data-[selected]:bg-accent flex w-full items-center gap-3 rounded-md px-3 py-2 text-left transition-colors"
    >
      <div
        className="bg-muted relative h-14 w-10 shrink-0 overflow-hidden rounded-md"
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
            onLoad={() => setLoaded(true)}
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
    </button>
  );
}

export function GlobalSearch({
  defaultOpen = false,
  initialQuery = "",
}: { defaultOpen?: boolean; initialQuery?: string } = {}) {
  const [open, setOpen] = useState(defaultOpen);
  const [query, setQuery] = useState(initialQuery);
  const [selectedIndex, setSelectedIndex] = useState(-1);
  const navigate = useViewTransitionNavigate();
  const debouncedQuery = useDebounce(query.trim(), DEBOUNCE_MS);
  const tmdbDebouncedQuery = useDebounce(query.trim(), TMDB_DEBOUNCE_MS);
  const canRequest = useCanRequest();
  const tmdbQuery = useRequestSearch("all", tmdbDebouncedQuery, 1, {
    enabled: canRequest.discoveryEnabled,
    requireProfile: true,
    staleTime: 5 * 60 * 1000,
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

  const searchState = useMemo(
    () => createCatalogSearchState("query", { q: debouncedQuery || undefined }),
    [debouncedQuery],
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
      limit: PREVIEW_LIMIT,
      offset: 0,
    }),
    queryFn: ({ signal }) => fetchCatalogPage(searchState, PREVIEW_LIMIT, 0, { signal }),
    enabled: open && debouncedQuery.length > 0,
    staleTime: 60 * 1000,
  });

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
      navigate(`/item/${encodeURIComponent(contentId)}`);
      setOpen(false);
      setQuery("");
    },
    [navigate],
  );

  // Reset selectedIndex when query changes
  useEffect(() => {
    setSelectedIndex(-1);
  }, [query]);

  // Auto-scroll the selected result into view
  useEffect(() => {
    if (selectedIndex >= 0) {
      document
        .getElementById(`search-result-${selectedIndex}`)
        ?.scrollIntoView({ block: "nearest" });
    }
  }, [selectedIndex]);

  const showResultsPanel = query.trim().length > 0;
  const items = previewQuery.data?.items ?? [];
  const total = previewQuery.data?.total ?? 0;
  const showLoading = previewQuery.isFetching && items.length === 0;
  const showEmpty =
    !previewQuery.isFetching &&
    debouncedQuery.length > 0 &&
    items.length === 0 &&
    !previewQuery.isError &&
    !tmdbStillLoading &&
    !tmdbWillRender &&
    !canRequest.isResolving &&
    !tmdbDebounceCatchingUp;
  const showError = previewQuery.isError;

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
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              placeholder="Search library..."
              className="placeholder:text-muted-foreground flex h-12 w-full bg-transparent text-sm outline-none"
              autoFocus
              aria-label="Search"
              aria-activedescendant={
                selectedIndex >= 0 ? `search-result-${selectedIndex}` : undefined
              }
              onKeyDown={(e) => {
                if (e.key === "ArrowDown") {
                  e.preventDefault();
                  setSelectedIndex((prev) =>
                    items.length === 0 ? -1 : prev < items.length - 1 ? prev + 1 : 0,
                  );
                } else if (e.key === "ArrowUp") {
                  e.preventDefault();
                  setSelectedIndex((prev) =>
                    items.length === 0 ? -1 : prev <= 0 ? items.length - 1 : prev - 1,
                  );
                } else if (e.key === "Enter" && selectedIndex >= 0 && items[selectedIndex]) {
                  e.preventDefault();
                  handlePickItem(items[selectedIndex].content_id);
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
              <div role="listbox">
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
                {items.map((item, i) => (
                  <GlobalSearchResultRow
                    key={item.content_id}
                    item={item}
                    index={i}
                    isSelected={i === selectedIndex}
                    onPick={handlePickItem}
                  />
                ))}
              </div>
              {tmdbDebouncedQuery.length > 1 && canRequest.discoveryEnabled && (
                <RequestToAddSection
                  variant="dialog"
                  query={tmdbDebouncedQuery}
                  libraryHadHits={items.length > 0}
                />
              )}
            </div>
            <div role="status" aria-live="polite" className="sr-only">
              {tmdbVisibleCount > 0
                ? `${items.length} library results, ${tmdbVisibleCount} request suggestions`
                : `${items.length} results found`}
            </div>
            <div className="text-muted-foreground border-t px-3 py-2 text-center text-xs">
              {total > PREVIEW_LIMIT ? (
                <p>
                  Showing top {PREVIEW_LIMIT} of {total}. Press Enter for all results.
                </p>
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
