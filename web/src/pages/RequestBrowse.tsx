import { useCallback } from "react";
import { useParams, useSearchParams } from "react-router";
import PageBack from "@/components/PageBack";
import RequestResultsGrid, {
  RequestResultsGridSkeleton,
  RequestResultsLoadMore,
} from "@/components/RequestResultsGrid";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { useRequestBrowse } from "@/hooks/queries/useRequests";
import { flattenResultPages, pendingPageSize } from "@/lib/mediaRequests";
import type { DiscoverBrowseKind, DiscoverBrowseResponse, RequestMediaType } from "@/api/types";

type BrowseSort = "popularity" | "vote_average" | "release_date";

const SORT_OPTIONS: { value: BrowseSort; label: string }[] = [
  { value: "popularity", label: "Popularity" },
  { value: "vote_average", label: "Rating" },
  { value: "release_date", label: "Release date" },
];

interface RequestBrowseProps {
  kind: DiscoverBrowseKind;
}

export default function RequestBrowse({ kind }: RequestBrowseProps) {
  const { slug = "" } = useParams<{ slug: string }>();
  const [searchParams, setSearchParams] = useSearchParams();

  const sort = normalizeSort(searchParams.get("sort"));
  const mediaTypeFromQuery = normalizeMediaType(searchParams.get("media_type"));
  const mediaType: RequestMediaType | undefined =
    kind === "studio" ? "movie" : kind === "network" ? "series" : (mediaTypeFromQuery ?? "movie");

  const browse = useRequestBrowse({ kind, slug, mediaType, sort });
  const firstPage = browse.data?.pages[0];

  const title = firstPage?.display_name ?? humanizeSlug(slug);
  useDocumentTitle(title ? `${title} - Requests` : "Requests");

  function updateSort(next: string) {
    const params = new URLSearchParams(searchParams);
    params.set("sort", next);
    params.delete("page");
    setSearchParams(params, { replace: true });
  }

  function updateMediaType(next: RequestMediaType) {
    const params = new URLSearchParams(searchParams);
    params.set("media_type", next);
    params.delete("page");
    setSearchParams(params, { replace: true });
  }

  const results = flattenResultPages(browse.data?.pages);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = browse;
  const loadMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);
  const kindLabel = kind === "studio" ? "Studio" : kind === "network" ? "Network" : "Genre";

  // A later page's failure keeps what loaded and offers a retry at the foot,
  // even when every loaded page was empty.
  const firstPageFailed = browse.isError && !browse.isFetchNextPageError && results.length === 0;

  if (firstPageFailed && (browse.error as { status?: number }).status === 404) {
    return (
      <div className="relative space-y-6 px-4 pt-6 pb-12 sm:px-6 lg:px-10 xl:px-12">
        <PageBack to="/requests" up />
        <h1 className="text-foreground mt-10 text-2xl font-bold tracking-tight sm:mt-12 sm:text-3xl">
          {kindLabel} not found.
        </h1>
      </div>
    );
  }

  // Laid out like the other "view all" grids (a Discover row, a
  // recommendation section): back link, title, then the poster grid.
  return (
    <div className="relative space-y-6 px-4 pt-6 pb-12 sm:px-6 lg:px-10 xl:px-12">
      <PageBack to="/requests" up />
      <header className="mt-10 flex flex-wrap items-end justify-between gap-4 sm:mt-12">
        <div className="flex min-w-0 items-center gap-4">
          <BrowseHeaderTile browse={firstPage} kind={kind} fallback={title} />
          <div className="min-w-0">
            <h1 className="text-foreground truncate text-2xl font-bold tracking-tight sm:text-3xl">
              {title}
            </h1>
            <p className="text-muted-foreground mt-1 text-sm">
              {browse.isLoading ? "Loading..." : kindLabel}
            </p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {kind === "genre" ? (
            <Tabs
              value={mediaType ?? "movie"}
              onValueChange={(value) => updateMediaType(value as RequestMediaType)}
            >
              <TabsList>
                <TabsTrigger value="movie">Movies</TabsTrigger>
                <TabsTrigger value="series">Series</TabsTrigger>
              </TabsList>
            </Tabs>
          ) : null}
          <Select value={sort} onValueChange={updateSort}>
            <SelectTrigger className="w-[160px]" aria-label="Sort by">
              <SelectValue placeholder="Sort" />
            </SelectTrigger>
            <SelectContent>
              {SORT_OPTIONS.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </header>

      {browse.isLoading ? (
        <RequestResultsGridSkeleton />
      ) : firstPageFailed ? (
        <p className="text-muted-foreground text-sm">
          Could not load this browse page. Try a different sort or media type.
        </p>
      ) : results.length === 0 && !hasNextPage ? (
        <p className="text-muted-foreground text-sm">Nothing matched. Try a different sort.</p>
      ) : (
        // A page can come back empty, e.g. when a profile's rating limit
        // filters out every title on it; the foot keeps loading.
        <>
          <RequestResultsGrid
            results={results}
            pendingCount={isFetchingNextPage ? pendingPageSize(browse.data?.pages) : 0}
          />
          <RequestResultsLoadMore
            hasNextPage={hasNextPage}
            isFetchingNextPage={isFetchingNextPage}
            isError={browse.isFetchNextPageError}
            onLoadMore={loadMore}
          />
        </>
      )}

      <ScrollToTopButton />
    </div>
  );
}

function BrowseHeaderTile({
  browse,
  kind,
  fallback,
}: {
  browse: DiscoverBrowseResponse | undefined;
  kind: DiscoverBrowseKind;
  fallback: string;
}) {
  if (!browse) {
    return <div className="bg-muted h-16 w-28 shrink-0 rounded-md" aria-hidden />;
  }
  if (kind === "genre") {
    return (
      <div
        className="bg-muted text-foreground flex h-16 w-28 shrink-0 items-center justify-center rounded-md px-2 text-center text-sm font-semibold"
        aria-hidden
      >
        {browse.display_name || fallback}
      </div>
    );
  }
  return (
    <div className="flex h-16 w-28 shrink-0 items-center justify-center overflow-hidden rounded-md bg-gray-800 ring-1 ring-gray-700">
      {browse.logo_url ? (
        <img
          src={browse.logo_url}
          alt={browse.display_name}
          className="h-full w-full object-contain p-2"
        />
      ) : (
        <span className="px-2 text-center text-xs font-semibold text-white">
          {browse.display_name || fallback}
        </span>
      )}
    </div>
  );
}

function normalizeSort(value: string | null): BrowseSort {
  return SORT_OPTIONS.some((option) => option.value === value)
    ? (value as BrowseSort)
    : "popularity";
}

function normalizeMediaType(value: string | null): RequestMediaType | undefined {
  return value === "movie" || value === "series" ? value : undefined;
}

function humanizeSlug(slug: string) {
  return slug
    .split("-")
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1))
    .join(" ");
}
