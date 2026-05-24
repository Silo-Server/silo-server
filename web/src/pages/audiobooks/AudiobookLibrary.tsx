import { useState } from "react";

import ScrollToTopButton from "@/components/ScrollToTopButton";
import { Skeleton } from "@/components/ui/skeleton";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useAudiobookLibrary } from "@/hooks/audiobooks/useAudiobookLibrary";

const PAGE_SIZE = 60;

const GRID_CLASSES =
  "grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8 gap-3";

export default function AudiobookLibrary() {
  const [offset, setOffset] = useState(0);
  const { data, isLoading, error } = useAudiobookLibrary({ limit: PAGE_SIZE, offset });

  if (isLoading && !data) {
    return (
      <div className="space-y-5 py-2 sm:space-y-6">
        <h1 className="text-3xl font-semibold">Audiobooks</h1>
        <div role="list" className={GRID_CLASSES}>
          {Array.from({ length: 24 }).map((_, i) => (
            <div key={i} role="listitem">
              <Skeleton className="aspect-[2/3] rounded-xl" />
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

  const items = data?.items ?? [];
  const total = data?.total ?? 0;

  return (
    <div className="space-y-5 py-2 sm:space-y-6">
      <h1 className="text-3xl font-semibold">Audiobooks</h1>
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
                <div className="media-card-image relative aspect-[2/3]">
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
      {total > PAGE_SIZE && (
        <div className="mt-8 flex justify-center gap-2">
          <button
            type="button"
            className="rounded-md border px-4 py-2 text-sm disabled:opacity-50"
            onClick={() => setOffset(Math.max(0, offset - PAGE_SIZE))}
            disabled={offset === 0}
          >
            Previous
          </button>
          <span className="text-muted-foreground self-center text-sm">
            {offset + 1}–{Math.min(offset + PAGE_SIZE, total)} of {total}
          </span>
          <button
            type="button"
            className="rounded-md border px-4 py-2 text-sm disabled:opacity-50"
            onClick={() => setOffset(offset + PAGE_SIZE)}
            disabled={offset + PAGE_SIZE >= total}
          >
            Next
          </button>
        </div>
      )}
      <ScrollToTopButton />
    </div>
  );
}
