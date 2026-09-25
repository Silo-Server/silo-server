import { useThemeMusic } from "./useThemeMusic";
import { useEffect, useState } from "react";
import { Navigate, useParams, useSearchParams } from "react-router";
import { useCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useUserLibraries } from "@/hooks/queries/libraries";
import type { ItemDetail } from "@/api/types";
import { isNotFoundProblem } from "@/api/v2/request";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { toast } from "sonner";
import MovieContent from "@/pages/ItemDetail/MovieContent";
import SeriesContent from "@/pages/ItemDetail/SeriesContent";
import SeasonContent from "@/pages/ItemDetail/SeasonContent";
import EpisodeContent from "@/pages/ItemDetail/EpisodeContent";
import AudiobookContent from "@/pages/ItemDetail/AudiobookContent";
import EbookContent from "@/pages/ItemDetail/EbookContent";
import MangaContent from "@/pages/ItemDetail/MangaContent";
import {
  CastSkeleton,
  CrewSkeleton,
  RecommendationGridSkeleton,
} from "./components/SectionSkeletons";
import {
  useSidebarItemDetailsReady,
  useSidebarItemEnteredFromHome,
} from "@/components/sidebarItemNavigationContext";
import { parseOptionalLibraryId } from "@/components/sidebarItemNavigation";
import { useShowAdvisoryAge } from "@/hooks/useShowAdvisoryAge";

function ItemDetailSkeleton() {
  return (
    <div>
      {/* Hero skeleton */}
      <section className="border-border/10 relative isolate overflow-hidden border-b">
        <div className="absolute inset-0 bg-gradient-to-r from-[var(--background)] via-[var(--background)]/70 to-transparent" />
        <div className="absolute inset-0 bg-gradient-to-t from-[var(--background)] via-[var(--background)]/40 to-transparent" />

        <div className="page-shell-wide relative flex min-h-[60dvh] flex-col justify-end pt-28 pb-8 lg:min-h-[72dvh]">
          <div className="flex flex-col gap-6 lg:flex-row lg:items-end">
            <Skeleton className="aspect-[2/3] w-[170px] flex-shrink-0 rounded-lg sm:w-[220px]" />

            <div className="max-w-3xl flex-1 space-y-4">
              <Skeleton className="h-4 w-16" />
              <Skeleton className="h-3 w-24" />
              <Skeleton className="h-10 w-80 max-w-full" />
              <div className="flex gap-2">
                <Skeleton className="h-6 w-14 rounded-md" />
                <Skeleton className="h-6 w-16 rounded-md" />
                <Skeleton className="h-6 w-20 rounded-md" />
              </div>
              <div className="flex gap-3">
                <Skeleton className="h-5 w-16" />
                <Skeleton className="h-5 w-16" />
              </div>
              <div className="space-y-2">
                <Skeleton className="h-4 w-full max-w-2xl" />
                <Skeleton className="h-4 w-5/6 max-w-xl" />
                <Skeleton className="h-4 w-3/4 max-w-lg" />
              </div>
              <Skeleton className="h-4 w-64" />
              <div className="flex gap-3 pt-2">
                <Skeleton className="h-10 w-28 rounded-full" />
                <Skeleton className="h-10 w-10 rounded-lg" />
                <Skeleton className="h-10 w-10 rounded-lg" />
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* Below-fold section skeletons */}
      <div className="page-shell space-y-12 py-10 sm:space-y-14">
        <CastSkeleton />
        <CrewSkeleton />
        <RecommendationGridSkeleton />
      </div>
    </div>
  );
}

interface HomeItemTransitionShape {
  compact: boolean;
  hidePoster: boolean;
  squarePoster: boolean;
  hasLogo: boolean;
}

const NEUTRAL_HOME_ITEM_TRANSITION_SHAPE: HomeItemTransitionShape = {
  compact: false,
  hidePoster: false,
  squarePoster: false,
  hasLogo: false,
};

function getHomeItemTransitionShape(item?: ItemDetail): HomeItemTransitionShape {
  if (!item) return NEUTRAL_HOME_ITEM_TRANSITION_SHAPE;

  return {
    compact: item.type === "season",
    hidePoster: item.type === "episode",
    squarePoster: item.type === "audiobook",
    hasLogo: Boolean(item.logo_url),
  };
}

function HomeItemTransitionShell({ item }: { item?: ItemDetail }) {
  const [shape] = useState(() => getHomeItemTransitionShape(item));
  const { compact, hidePoster, squarePoster, hasLogo } = shape;

  return (
    <div
      data-testid="home-item-transition-shell"
      className="bg-background min-h-screen"
      aria-busy="true"
    >
      <span className="sr-only">Loading item details</span>
      <section aria-hidden="true" className="border-border/10 border-b">
        <div
          className={`page-shell-wide flex flex-col justify-end pb-8 ${
            compact
              ? "min-h-[max(35vh,300px)] pt-20 lg:min-h-[42vh]"
              : "min-h-[60dvh] pt-28 lg:min-h-[72dvh]"
          }`}
        >
          <div className={`flex flex-col gap-6 ${!hidePoster ? "lg:flex-row lg:items-end" : ""}`}>
            {!hidePoster && (
              <div
                data-testid="home-item-transition-poster"
                className={`home-item-transition-block flex-shrink-0 rounded-lg border border-solid shadow-[var(--shadow-md)] ${
                  squarePoster
                    ? compact
                      ? "aspect-square w-[180px] sm:w-[200px]"
                      : "aspect-square w-[200px] sm:w-[260px]"
                    : compact
                      ? "aspect-[2/3] w-[140px] sm:w-[160px]"
                      : "aspect-[2/3] w-[170px] sm:w-[220px]"
                }`}
              />
            )}

            <div className="max-w-3xl flex-1">
              <div className="home-item-transition-block mb-4 h-3 w-16 rounded-full" />
              <div
                data-testid="home-item-transition-title"
                className={`home-item-transition-block mb-4 rounded-lg ${
                  hasLogo
                    ? "h-20 w-full max-w-[420px] lg:h-28 lg:max-w-[480px]"
                    : compact
                      ? "h-10 w-full max-w-sm sm:h-11"
                      : "h-12 w-full max-w-lg sm:h-14 lg:h-16"
                }`}
              />
              <div className="home-item-transition-block mb-4 h-5 w-52 max-w-full rounded-md" />
              <div className="home-item-transition-block mb-4 h-4 w-36 rounded-md" />
              <div className="max-w-2xl space-y-2">
                <div className="home-item-transition-block h-4 w-full rounded-md" />
                <div className="home-item-transition-block h-4 w-4/5 rounded-md" />
              </div>
              <div className="home-item-transition-block mt-6 h-10 w-32 rounded-full" />
            </div>
          </div>
        </div>
      </section>
    </div>
  );
}

/**
 * The server answers the same 404 for a missing item and one outside the
 * viewer's libraries, so this says neither. The link's library is offered only
 * when the viewer can still open it.
 */
function ItemUnavailable({ libraryId }: { libraryId?: number }) {
  const { data: libraries, dataUpdatedAt, isFetching, isError, refetch } = useUserLibraries();
  const [shownAt] = useState(() => Date.now());
  // The 404 may mean the viewer just lost this library, which a cached list
  // would still offer. Only a read that succeeded after this page appeared
  // can vouch for it; a failed read leaves the old list, so it fails closed.
  useEffect(() => {
    if (libraryId !== undefined) void refetch();
  }, [libraryId, refetch]);
  const confirmed = !isFetching && !isError && dataUpdatedAt >= shownAt;
  const library =
    libraryId === undefined || !confirmed
      ? undefined
      : libraries?.find((entry) => entry.id === libraryId);

  return (
    <PageUnavailable
      title="This item isn't available"
      description="It may have been removed, or you may not have access to it."
    >
      {library && (
        <Button asChild variant="outline">
          <ViewTransitionLink to={`/library/${library.id}`}>Browse library</ViewTransitionLink>
        </Button>
      )}
    </PageUnavailable>
  );
}

export default function ItemDetail() {
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const libraryId = parseOptionalLibraryId(searchParams.get("libraryId"));
  const {
    data: cachedItem,
    isLoading: loading,
    isFetching,
    error: itemError,
    refetch,
  } = useCatalogItemDetail(id, libraryId);
  const itemNotFound = isNotFoundProblem(itemError);
  // A 404 outranks cached detail. A refetch that finds the item gone, or out
  // of the viewer's reach, leaves the old item in the cache, and that page
  // must not stay up.
  const item = itemNotFound ? undefined : cachedItem;
  const itemDetailsReady = useSidebarItemDetailsReady();
  // Resolved once here rather than in each content component: the badge is a
  // display choice, and a leaf component should not have to fetch to render.
  // Items without an advisory skip the lookup, which is most of them.
  const showAdvisoryAge = useShowAdvisoryAge(item?.advisory_age != null);
  const enteredItemFromHome = useSidebarItemEnteredFromHome();

  useDocumentTitle(itemNotFound ? "Not found" : (item?.title ?? "Item"));
  useThemeMusic(item, loading);

  useEffect(() => {
    // A 404 has the page to itself; a toast would only repeat it.
    if (itemError && !isNotFoundProblem(itemError)) {
      toast.error(itemError instanceof Error ? itemError.message : "Failed to load item");
    }
  }, [itemError]);

  if (loading || !itemDetailsReady) {
    if (enteredItemFromHome) {
      return <HomeItemTransitionShell key={id} item={item} />;
    }
    return <ItemDetailSkeleton />;
  }

  if (!item) {
    if (itemError && !itemNotFound) {
      return (
        <PageUnavailable
          title="Couldn't load this item"
          description="Something went wrong while loading it. Try again in a moment."
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    // Keyed by the URL so a new item or library starts a new confirmation
    // window; a cached 404 lets navigation reuse this view without a remount.
    return <ItemUnavailable key={`${id}:${libraryId ?? ""}`} libraryId={libraryId} />;
  }

  switch (item.type) {
    case "movie":
      return (
        <MovieContent
          item={item as ItemDetail & { type: "movie" }}
          showAdvisoryAge={showAdvisoryAge}
        />
      );
    case "series":
      return (
        <SeriesContent
          item={item as ItemDetail & { type: "series" }}
          showAdvisoryAge={showAdvisoryAge}
        />
      );
    case "season":
      return <SeasonContent item={item as ItemDetail & { type: "season" }} />;
    case "episode":
      return <EpisodeContent item={item as ItemDetail & { type: "episode" }} />;
    case "audiobook":
      return (
        <AudiobookContent item={item as ItemDetail & { type: "audiobook" }} libraryId={libraryId} />
      );
    case "ebook":
      return (
        <EbookContent
          item={item as ItemDetail & { type: "ebook" }}
          libraryId={libraryId}
          showAdvisoryAge={showAdvisoryAge}
        />
      );
    case "manga":
      return (
        <MangaContent
          item={item as ItemDetail & { type: "manga" }}
          libraryId={libraryId}
          showAdvisoryAge={showAdvisoryAge}
        />
      );
    case "podcast":
      return <Navigate to={`/podcasts/show/${item.content_id}`} replace />;
    default:
      return (
        <div className="page-shell text-muted-foreground py-8">
          Unsupported item type: {item.type}
        </div>
      );
  }
}
