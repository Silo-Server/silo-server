import { useSearchParams } from "react-router";
import { X } from "lucide-react";
import ScrollToTopButton from "@/components/ScrollToTopButton";
import { Skeleton } from "@/components/ui/skeleton";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useInfiniteAudiobookLibrary } from "@/hooks/audiobooks/useAudiobookLibrary";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";

const PAGE_SIZE = 60;

const GRID_CLASSES =
  "grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8 gap-3";

export default function AudiobookLibrary() {
  const [searchParams, setSearchParams] = useSearchParams();
  const genre = searchParams.get("genre") ?? "";
  const { data, isLoading, error, fetchNextPage, hasNextPage, isFetchingNextPage } =
    useInfiniteAudiobookLibrary({ limit: PAGE_SIZE, genre });

  const sentinelRef = useIntersectionObserver({
    onIntersect: fetchNextPage,
    enabled: Boolean(hasNextPage) && !isFetchingNextPage,
  });

  if (isLoading && !data) {
    return (
      <div className="space-y-5 py-2 sm:space-y-6">
        <h1 className="text-3xl font-semibold">Audiobooks</h1>
        <div role="list" className={GRID_CLASSES}>
          {Array.from({ length: 24 }).map((_, i) => (
            <div key={i} role="listitem">
              <Skeleton className="aspect-square rounded-xl" />
              <Skeleton className="mt-2 h-4 w-3/4" />
            </div>
          ))}
        </div>
      </div>
    );
  }

  if (error) {
    return (
      <div className="space-y-5 py-2 sm:space-y-6">
        <h1 className="text-3xl font-semibold">Audiobooks</h1>
        <div className="text-destructive py-12 text-center">
          Failed to load audiobooks: {error instanceof Error ? error.message : "Unknown error"}
        </div>
      </div>
    );
  }

  const items = data?.pages.flatMap((p) => p.items) ?? [];
  const total = data?.pages[0]?.total ?? 0;

  return (
    <div className="space-y-5 py-2 sm:space-y-6">
      <div className="flex items-baseline justify-between gap-4">
        <div className="flex flex-wrap items-baseline gap-3">
          <h1 className="text-3xl font-semibold">Audiobooks</h1>
          {genre && (
            <button
              type="button"
              onClick={() => {
                const next = new URLSearchParams(searchParams);
                next.delete("genre");
                setSearchParams(next);
              }}
              className="bg-muted hover:bg-muted/80 inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-medium"
            >
              {genre}
              <X className="h-3 w-3" />
            </button>
          )}
        </div>
        {total > 0 && (
          <span className="text-muted-foreground text-sm">
            {items.length.toLocaleString()} of {total.toLocaleString()}
          </span>
        )}
      </div>
      {items.length === 0 ? (
        <div className="text-muted-foreground py-12 text-center">
          No audiobooks indexed yet. Set a library&apos;s type to{" "}
          <code className="text-foreground bg-muted rounded px-1 py-0.5 text-sm">audiobooks</code>{" "}
          and rescan.
        </div>
      ) : (
        <div role="list" className={GRID_CLASSES}>
          {items.map((item) => (
            <div key={item.content_id} role="listitem" className="media-card group/card">
              <ViewTransitionLink
                to={`/audiobooks/book/${item.content_id}`}
                className="block overflow-hidden rounded-xl"
              >
                <div className="media-card-image relative aspect-square">
                  {item.poster_url ? (
                    <img
                      src={item.poster_url}
                      alt={item.title}
                      className="h-full w-full object-cover"
                      loading="lazy"
                    />
                  ) : (
                    <div className="text-muted-foreground flex h-full w-full flex-col items-center justify-center gap-1 p-3 text-center text-sm">
                      <span className="line-clamp-3 font-medium">{item.title || "No Cover"}</span>
                    </div>
                  )}
                  <div className="from-background/70 pointer-events-none absolute inset-x-0 bottom-0 h-24 bg-gradient-to-t to-transparent opacity-90" />
                </div>
              </ViewTransitionLink>
              <ViewTransitionLink
                to={`/audiobooks/book/${item.content_id}`}
                className="block px-1 pt-3"
              >
                <div className="truncate text-[14px] font-semibold tracking-tight">
                  {item.title}
                </div>
                {item.year > 0 && (
                  <div className="text-muted-foreground mt-1 text-[11px] font-medium tracking-[0.14em] uppercase">
                    {item.year}
                  </div>
                )}
              </ViewTransitionLink>
            </div>
          ))}
        </div>
      )}
      {hasNextPage && (
        <div ref={sentinelRef} className="flex justify-center py-8">
          {isFetchingNextPage && (
            <span className="text-muted-foreground text-sm">Loading more…</span>
          )}
        </div>
      )}
      <ScrollToTopButton />
    </div>
  );
}
