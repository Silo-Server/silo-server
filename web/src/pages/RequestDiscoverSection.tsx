import { useCallback } from "react";
import { useParams } from "react-router";
import { RefreshCw } from "lucide-react";
import { V2ProblemError } from "@/api/v2/request";
import PageBack from "@/components/PageBack";
import RequestResultsGrid, {
  RequestResultsGridSkeleton,
  RequestResultsLoadMore,
} from "@/components/RequestResultsGrid";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import { useRequestDiscoverySection } from "@/hooks/queries/useRequests";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { flattenResultPages, pendingPageSize } from "@/lib/mediaRequests";

/**
 * Every title of one Discover row (Trending Movies, Popular Series, ...),
 * loaded as the viewer scrolls: the hub's "Explore all" destination.
 */
export default function RequestDiscoverSection() {
  const { section = "" } = useParams<{ section: string }>();
  const query = useRequestDiscoverySection(section);

  const title = query.data?.pages[0]?.title || humanizeSectionKey(section);
  useDocumentTitle(`${title} - Requests`);

  const results = flattenResultPages(query.data?.pages);
  // A later page's failure keeps what loaded and offers a retry at the foot,
  // even when every loaded page was empty.
  const firstPageFailed = query.isError && !query.isFetchNextPageError && results.length === 0;
  const unknownSection =
    firstPageFailed &&
    query.error instanceof V2ProblemError &&
    [400, 404, 422].includes(query.error.status);
  const { hasNextPage, isFetchingNextPage, fetchNextPage } = query;
  const loadMore = useCallback(() => {
    if (hasNextPage && !isFetchingNextPage) void fetchNextPage();
  }, [fetchNextPage, hasNextPage, isFetchingNextPage]);

  return (
    <div className="relative space-y-6 px-4 pt-6 pb-12 sm:px-6 lg:px-10 xl:px-12">
      <PageBack to="/requests" up />
      <header className="mt-10 flex flex-col gap-1.5 sm:mt-12">
        <h1 className="text-foreground text-2xl font-bold tracking-tight sm:text-3xl">
          {unknownSection ? "Not found" : title}
        </h1>
      </header>

      {query.isLoading ? (
        <RequestResultsGridSkeleton />
      ) : unknownSection ? (
        <p className="text-muted-foreground text-sm">This Discover row doesn&rsquo;t exist.</p>
      ) : firstPageFailed ? (
        <div className="flex flex-col items-center justify-center gap-4 py-24 text-center">
          <p className="text-muted-foreground text-sm">
            TMDB couldn&rsquo;t be reached. Try again in a moment.
          </p>
          <button
            type="button"
            onClick={() => void query.refetch()}
            className="text-primary hover:text-primary/80 inline-flex items-center gap-2 text-sm font-medium"
          >
            <RefreshCw className="h-4 w-4" />
            Retry
          </button>
        </div>
      ) : results.length === 0 && !hasNextPage ? (
        <p className="text-muted-foreground text-sm">Nothing here right now.</p>
      ) : (
        // A page can come back empty, e.g. when a profile's rating limit
        // filters out every title the server read; the foot keeps loading.
        <>
          <RequestResultsGrid
            results={results}
            pendingCount={isFetchingNextPage ? pendingPageSize(query.data?.pages) : 0}
          />
          <RequestResultsLoadMore
            hasNextPage={hasNextPage}
            isFetchingNextPage={isFetchingNextPage}
            isError={query.isFetchNextPageError}
            onLoadMore={loadMore}
          />
        </>
      )}
      <ScrollToTopButton />
    </div>
  );
}

/** "trending_movies" → "Trending movies", until the server's title arrives. */
function humanizeSectionKey(key: string): string {
  const words = key.split(/[_-]+/).filter(Boolean).join(" ");
  return words ? words.charAt(0).toUpperCase() + words.slice(1) : "Discover";
}
