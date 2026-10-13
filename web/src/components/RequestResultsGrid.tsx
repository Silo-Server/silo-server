import type { RequestMediaResult } from "@/api/types";
import RequestPosterCard from "@/components/RequestPosterCard";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import { useSubmitMediaRequest } from "@/hooks/useSubmitMediaRequest";
import { useUICustomization } from "@/hooks/useUICustomization";
import { useWatchlistTitleToggle } from "@/hooks/useWatchlistTitleToggle";
import { cardGridClasses } from "@/lib/uiCustomization";
import { cn } from "@/lib/utils";

/**
 * TMDB titles on the library's poster grid, each with the hover Request
 * and watchlist actions. Used by the Discover, studio/network/genre, and search "Request to
 * add" grids.
 */
export default function RequestResultsGrid({
  results,
  pendingCount = 0,
  className,
}: {
  results: RequestMediaResult[];
  /**
   * Placeholder cards for a page that is loading. They continue the last row
   * rather than starting a grid of their own, so the grid does not jump when
   * the titles arrive.
   */
  pendingCount?: number;
  className?: string;
}) {
  const { cardPresentation } = useUICustomization();
  const { submit, isSubmitting } = useSubmitMediaRequest();
  const watchlist = useWatchlistTitleToggle();
  return (
    <div className={cn(cardGridClasses(cardPresentation.poster_size), className)}>
      {results.map((item) => (
        <RequestPosterCard
          key={`${item.media_type}-${item.tmdb_id}`}
          variant="discover"
          item={item}
          isSubmitting={isSubmitting(item)}
          onRequest={() => submit(item)}
          onToggleWatchlist={watchlist.enabled ? () => watchlist.toggle(item) : undefined}
          isWatchlistPending={watchlist.isPending(item)}
          fluid
        />
      ))}
      {Array.from({ length: pendingCount }).map((_, index) => (
        <PosterCardSkeleton key={`pending-${index}`} />
      ))}
    </div>
  );
}

function PosterCardSkeleton() {
  return (
    <div aria-hidden>
      <Skeleton className="aspect-[2/3] w-full rounded-xl" />
      <Skeleton className="mt-3 h-4 w-3/4 rounded" />
      <Skeleton className="mt-1.5 h-3 w-1/2 rounded" />
    </div>
  );
}

export function RequestResultsGridSkeleton({ count = 18 }: { count?: number }) {
  const { cardPresentation } = useUICustomization();
  return (
    <div className={cardGridClasses(cardPresentation.poster_size)} aria-hidden>
      {Array.from({ length: count }).map((_, index) => (
        <PosterCardSkeleton key={index} />
      ))}
    </div>
  );
}

/**
 * The foot of a TMDB result grid that loads as the viewer scrolls: reaching
 * it reads the next page, and a failed read offers a retry. The grid shows the
 * loading page's placeholders (RequestResultsGrid pendingCount). Without
 * IntersectionObserver a Load more button does the scrolling's job.
 */
export function RequestResultsLoadMore({
  hasNextPage,
  isFetchingNextPage,
  isError,
  onLoadMore,
}: {
  hasNextPage: boolean;
  isFetchingNextPage: boolean;
  /** The last next-page read failed. */
  isError: boolean;
  onLoadMore: () => void;
}) {
  const sentinelRef = useIntersectionObserver({
    onIntersect: onLoadMore,
    enabled: hasNextPage && !isFetchingNextPage && !isError,
  });
  if (!hasNextPage) return null;
  if (isFetchingNextPage) return null;
  if (isError) {
    return (
      <div className="flex flex-col items-center gap-3 py-6 text-center">
        <p className="text-muted-foreground text-sm">Couldn&rsquo;t load more titles.</p>
        <Button variant="outline" size="sm" onClick={onLoadMore}>
          Try again
        </Button>
      </div>
    );
  }
  return (
    <div ref={sentinelRef} className="flex justify-center py-4">
      {typeof IntersectionObserver === "undefined" ? (
        <Button variant="outline" size="sm" onClick={onLoadMore}>
          Load more
        </Button>
      ) : null}
    </div>
  );
}

/**
 * Previous / Next paging for a TMDB result list. The caller decides where
 * each button leads: plain page numbers, or a server cursor that can skip
 * pages. Renders nothing when there is nowhere to go.
 */
export function RequestResultsPager({
  position,
  hasPrevious,
  hasNext,
  onPrevious,
  onNext,
  disabled = false,
  label = "Result pages",
}: {
  /** Where the viewer is, such as "Page 2 of 9". Omitted when it isn't known. */
  position?: string;
  hasPrevious: boolean;
  hasNext: boolean;
  onPrevious: () => void;
  onNext: () => void;
  disabled?: boolean;
  label?: string;
}) {
  if (!hasPrevious && !hasNext) return null;
  return (
    <nav aria-label={label} className="flex items-center justify-center gap-3 pt-2">
      <Button variant="outline" size="sm" disabled={disabled || !hasPrevious} onClick={onPrevious}>
        Previous
      </Button>
      {position ? (
        <span className="text-muted-foreground text-sm tabular-nums">{position}</span>
      ) : null}
      <Button variant="outline" size="sm" disabled={disabled || !hasNext} onClick={onNext}>
        Next
      </Button>
    </nav>
  );
}
