import { Link } from "react-router";
import { ThumbsDown } from "lucide-react";
import type { BrowseItem } from "@/api/types";
import { Button } from "@/components/ui/button";
import { useNotInterestedList, useToggleNotInterested } from "@/hooks/queries/notInterested";
import { buildItemHref } from "@/lib/mediaNavigation";

function NotInterestedRow({ item }: { item: BrowseItem }) {
  const { mutate: toggle, isPending } = useToggleNotInterested(item.content_id);
  return (
    <li className="grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-2 py-2 sm:grid-cols-[auto_minmax(0,1fr)_auto]">
      <Link
        to={buildItemHref({ contentId: item.content_id })}
        className="bg-muted row-span-2 h-16 w-11 overflow-hidden rounded sm:row-span-1"
      >
        {item.poster_url && (
          <img src={item.poster_url} alt="" className="h-full w-full object-cover" loading="lazy" />
        )}
      </Link>
      <div className="min-w-0">
        <Link
          to={buildItemHref({ contentId: item.content_id })}
          className="block truncate text-sm font-medium hover:underline"
        >
          {item.title}
        </Link>
        <span className="text-muted-foreground text-xs">
          {[item.year > 0 ? item.year : null, item.type === "series" ? "Series" : "Movie"]
            .filter(Boolean)
            .join(" · ")}
        </span>
      </div>
      <Button
        variant="outline"
        size="sm"
        className="justify-self-start sm:justify-self-end"
        disabled={isPending}
        onClick={() => toggle(true)}
      >
        Show in Recommendations
      </Button>
    </li>
  );
}

/**
 * The titles the profile marked Not interested, newest first, each with a
 * way to put it back in recommendations.
 */
export function NotInterestedList() {
  const {
    data,
    isPending,
    isError,
    isFetchNextPageError,
    hasNextPage,
    fetchNextPage,
    isFetchingNextPage,
    refetch,
    isRefetching,
  } = useNotInterestedList();
  const items = data?.pages.flatMap((page) => page.items) ?? [];

  return (
    <div className="surface-panel rounded-2xl border-0 p-6">
      <div className="flex items-start gap-4">
        <div className="bg-primary/10 text-primary flex h-12 w-12 shrink-0 items-center justify-center rounded-full">
          <ThumbsDown className="h-6 w-6" />
        </div>
        <div className="min-w-0 flex-1 space-y-3">
          <div>
            <h3 className="text-base font-semibold">Not interested</h3>
            <p className="text-muted-foreground mt-1 text-sm">
              Titles you marked Not interested stay out of your recommendations, and similar titles
              rank lower. You can still find and play them anywhere else.
            </p>
          </div>
          {isPending ? (
            <p className="text-muted-foreground text-sm">Loading…</p>
          ) : !data ? (
            <div className="flex items-center gap-3">
              <p className="text-muted-foreground text-sm">
                Couldn't load your Not interested titles.
              </p>
              <Button
                variant="outline"
                size="sm"
                disabled={isRefetching}
                onClick={() => void refetch()}
              >
                Retry
              </Button>
            </div>
          ) : (
            <>
              {items.length > 0 ? (
                <ul className="divide-border divide-y">
                  {items.map((item) => (
                    <NotInterestedRow key={item.content_id} item={item} />
                  ))}
                </ul>
              ) : (
                !hasNextPage && (
                  <p className="text-muted-foreground text-sm">
                    Nothing yet. Choose Not Interested from a movie or show's menu.
                  </p>
                )
              )}
              {isError && !isFetchNextPageError && (
                <div className="flex items-center gap-3">
                  <p className="text-muted-foreground text-sm">
                    Couldn't refresh this list; it may be out of date.
                  </p>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={isRefetching}
                    onClick={() => void refetch()}
                  >
                    Retry
                  </Button>
                </div>
              )}
              {hasNextPage && (
                <div className="flex items-center gap-3">
                  <Button
                    variant="ghost"
                    size="sm"
                    disabled={isFetchingNextPage}
                    onClick={() => void fetchNextPage()}
                  >
                    {isFetchingNextPage
                      ? "Loading…"
                      : isFetchNextPageError
                        ? "Retry loading more"
                        : "Show more"}
                  </Button>
                  {isFetchNextPageError && !isFetchingNextPage && (
                    <span className="text-muted-foreground text-sm">
                      Couldn't load more titles.
                    </span>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  );
}
