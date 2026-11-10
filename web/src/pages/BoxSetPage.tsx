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
import { useAppLanguage, useT } from "@/i18n";
import { buildMediaPlayHref } from "@/lib/mediaNavigation";
import DetailHero from "@/pages/ItemDetail/DetailHero";
import DetailLayout, { DetailSection } from "@/pages/ItemDetail/DetailLayout";

/**
 * One movie box set: the movies TMDB lists in one collection that the viewer
 * can see in one library, in release order.
 */
export default function BoxSetPage() {
  const { id } = useParams<{ id: string }>();
  const t = useT();
  // The film count comes from a plain helper; follow language changes.
  useAppLanguage();
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
          title={t("web.detail.boxSet.loadFailedTitle")}
          description={t("web.detail.boxSet.loadFailedBody")}
          onRetry={() => void refetch()}
          retrying={isFetching}
        />
      );
    }
    return (
      <PageUnavailable
        title={t("web.detail.boxSet.unavailableTitle")}
        description={t("web.detail.boxSet.unavailableBody")}
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
          context={t("web.detail.boxSet.label")}
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
                  {t("web.detail.boxSet.watchedOf", {
                    played: String(boxSet.played_count),
                    total: String(boxSet.movie_count),
                  })}
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
                    <span className="min-w-0 truncate">
                      {t(
                        started ? "web.detail.boxSet.continueTitle" : "web.detail.boxSet.playTitle",
                        { title: next.title },
                      )}
                    </span>
                  </ViewTransitionLink>
                </Button>
              </div>
            ) : undefined
          }
        />
      }
    >
      <DetailSection title={t("web.detail.boxSet.movies")}>
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
