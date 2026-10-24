import { Play } from "lucide-react";
import { useParams, useSearchParams } from "react-router";

import { isNotFoundProblem } from "@/api/v2/request";
import ItemGrid from "@/components/ItemGrid";
import PageBack from "@/components/PageBack";
import PageUnavailable from "@/components/PageUnavailable";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { useBoxSet } from "@/hooks/queries/boxSets";
import { boxSetFilmCount, boxSetNextMovie, boxSetYears } from "@/lib/boxSets";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import DetailLayout, { DetailSection } from "@/pages/ItemDetail/DetailLayout";

/**
 * One movie box set: the movies TMDB lists in one collection that the viewer
 * can see in one library, in release order.
 */
export default function BoxSetPage() {
  const { id } = useParams<{ id: string }>();
  const [searchParams] = useSearchParams();
  const libraryId = searchParams.get("library_id") ?? undefined;
  const { data: boxSet, isLoading, error, refetch, isFetching } = useBoxSet(id, libraryId);
  useDocumentTitle(boxSet?.title);

  if (isLoading) {
    return <BoxSetSkeleton />;
  }
  if (!boxSet) {
    if (error && !isNotFoundProblem(error)) {
      return (
        <PageUnavailable
          title="Couldn't load this box set"
          description="Something went wrong while loading it. Try again in a moment."
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    return (
      <PageUnavailable
        title="This box set isn't available"
        description="Box sets may be turned off for this library, or too few of its movies are here."
      />
    );
  }

  const next = boxSetNextMovie(boxSet.items);
  const started = boxSet.played_count > 0 && boxSet.played_count < boxSet.movie_count;
  const numericLibraryId = Number(boxSet.library_id) || undefined;
  const years = boxSetYears(boxSet);

  return (
    <DetailLayout
      hero={
        <DetailHero
          title={boxSet.title}
          topNav={<PageBack />}
          context="Box set"
          shortHero
          backdropUrl={boxSet.backdrop_url}
          backdropThumbhash={boxSet.backdrop_thumbhash}
          posterUrl={boxSet.poster_url}
          posterThumbhash={boxSet.poster_thumbhash}
          metadata={
            <div className="flex flex-wrap items-center gap-2">
              <span className="metadata-badge">{boxSetFilmCount(boxSet.movie_count)}</span>
              {years ? <span className="metadata-badge">{years}</span> : null}
              {boxSet.played_count > 0 ? (
                <span className="metadata-badge">
                  {boxSet.played_count} of {boxSet.movie_count} watched
                </span>
              ) : null}
            </div>
          }
          overview={boxSet.overview || undefined}
          actions={
            next ? (
              <div className="detail-primary-actions flex flex-wrap items-center gap-3">
                <Button
                  asChild
                  className="relative h-11 max-w-full cursor-pointer gap-2.5 overflow-hidden rounded-full px-8 text-[0.9375rem] font-bold tracking-wide shadow-md"
                >
                  <ViewTransitionLink
                    to={buildMediaPlayHref({
                      contentId: next.content_id,
                      type: "movie",
                      libraryId: numericLibraryId,
                    })}
                  >
                    <Play className="size-[18px] fill-current" aria-hidden />
                    {/* A film title can be wider than a phone; the label
                        truncates rather than widen the hero past the screen. */}
                    <span className="truncate">
                      {started ? `Continue · ${next.title}` : `Play ${next.title}`}
                    </span>
                  </ViewTransitionLink>
                </Button>
              </div>
            ) : undefined
          }
        />
      }
    >
      <DetailSection title="Movies">
        <ItemGrid items={boxSet.items} libraryId={numericLibraryId} />
      </DetailSection>
    </DetailLayout>
  );
}

function BoxSetSkeleton() {
  return (
    <div className="page-shell pt-8 sm:pt-10">
      <div className="mt-10 flex gap-6 sm:mt-12">
        <Skeleton className="aspect-[2/3] w-[140px] shrink-0 rounded-xl sm:w-[180px]" />
        <div className="flex-1 space-y-3 pt-8">
          <Skeleton className="h-3 w-40" />
          <Skeleton className="h-9 w-2/3" />
          <Skeleton className="h-16 w-full max-w-2xl" />
        </div>
      </div>
    </div>
  );
}
