import { useMemo } from "react";
import { Bookmark, Compass, RefreshCw } from "lucide-react";
import type { WatchlistTitle } from "@/api/v2/watchlistTitles";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import WatchlistTitleCard from "@/components/watchlist/WatchlistTitleCard";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { useSubmitMediaRequest } from "@/hooks/useSubmitMediaRequest";
import { useWatchlistTitleToggle } from "@/hooks/useWatchlistTitleToggle";
import { cardGridClasses } from "@/lib/uiCustomization";
import {
  sortWatchlistTitlesSoonestFirst,
  watchlistTitlesHint,
  watchlistTitleStatus,
} from "@/lib/watchlistTitles";

const DISCOVER_HREF = "/requests";

/**
 * The watchlist's "Not in your library yet" tab: titles saved from Discover
 * that the library doesn't have, soonest first, each opening its Discover
 * title page.
 */
export default function WatchlistTitlesTab({
  titles,
  isLoading,
  isError,
  onRetry,
  watchlistRequests,
}: {
  titles: WatchlistTitle[] | undefined;
  isLoading: boolean;
  isError: boolean;
  onRetry: () => void;
  /** Adding a title requests it for this viewer (FeatureStatus.watchlist_requests). */
  watchlistRequests: boolean;
}) {
  const { prefs: overlayPrefs } = useOverlayPrefs();
  const { submit, isSubmitting } = useSubmitMediaRequest();
  const watchlist = useWatchlistTitleToggle();
  const entries = useMemo(() => {
    const now = new Date();
    return sortWatchlistTitlesSoonestFirst(titles ?? [], now).map((title) => ({
      title,
      status: watchlistTitleStatus(title, now),
    }));
  }, [titles]);

  if (isError) {
    return (
      <div
        className="search-paint-surface flex flex-col items-center justify-center gap-3 rounded-2xl border px-4 py-16 text-center"
        role="alert"
      >
        <p className="font-medium">Could not load these titles.</p>
        <Button variant="outline" size="sm" onClick={onRetry}>
          <RefreshCw className="size-4" />
          Retry
        </Button>
      </div>
    );
  }

  if (isLoading) {
    return (
      <div className={cardGridClasses("large")} aria-hidden>
        {Array.from({ length: 6 }).map((_, index) => (
          <Skeleton key={index} className="aspect-[2/3] w-full rounded-xl" />
        ))}
      </div>
    );
  }

  if (entries.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center gap-3 rounded-2xl border border-dashed px-4 py-16 text-center">
        <Bookmark className="text-muted-foreground size-8" strokeWidth={1.5} aria-hidden />
        <p className="font-medium">Nothing waiting for the library</p>
        <p className="text-muted-foreground max-w-md text-sm">
          Add movies and series from Discover to your watchlist. The ones the library doesn&rsquo;t
          have yet wait here until they arrive.
        </p>
        <Button asChild variant="outline" size="sm">
          <ViewTransitionLink to={DISCOVER_HREF}>
            <Compass className="size-4" />
            Browse Discover
          </ViewTransitionLink>
        </Button>
      </div>
    );
  }

  return (
    <div className="space-y-6">
      <section className="surface-panel flex flex-col gap-2 rounded-2xl border-0 p-4 sm:flex-row sm:items-center sm:justify-between sm:gap-4">
        <p className="text-muted-foreground text-sm">{watchlistTitlesHint(watchlistRequests)}</p>
        <ViewTransitionLink
          to={DISCOVER_HREF}
          className="text-primary hover:text-primary/80 shrink-0 text-sm font-medium"
        >
          Find more in Discover
        </ViewTransitionLink>
      </section>
      <div className={cardGridClasses("large")}>
        {entries.map(({ title, status }) => (
          <WatchlistTitleCard
            key={`${title.media_type}-${title.tmdb_id}`}
            title={title}
            status={status}
            overlayPrefs={overlayPrefs}
            onRequest={() =>
              submit({
                media_type: title.media_type,
                tmdb_id: title.tmdb_id,
                title: title.title,
                year: title.year,
                poster_path: title.poster_path,
                availability: "missing",
                request: title.request,
              })
            }
            isRequesting={isSubmitting(title)}
            onRemove={() => watchlist.toggle({ ...title, in_watchlist: true })}
            isRemoving={watchlist.isPending(title)}
          />
        ))}
      </div>
    </div>
  );
}
