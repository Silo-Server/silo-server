import { useMemo } from "react";
import { Check, ChevronLeft, ChevronRight } from "lucide-react";
import type { Season } from "@/api/types";
import CardPlayOverlay from "@/components/CardPlayOverlay";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { usePrefetchCatalogSeason } from "@/hooks/queries/catalogRead";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { useDwellPrefetch } from "@/hooks/useDwellPrefetch";
import {
  formatSeasonMeta,
  getSeasonDisplayTitle,
  type SeasonNavigationState,
} from "./itemDetailLayout";

interface SeasonCarouselProps {
  seasons: Season[];
  parentSeriesHref?: string;
}

export default function SeasonCarousel({ seasons, parentSeriesHref }: SeasonCarouselProps) {
  const sorted = useMemo(
    () => seasons.slice().sort((a, b) => a.season_number - b.season_number),
    [seasons],
  );
  const { emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } = useCarouselEmbla();
  const prefetchSeason = usePrefetchCatalogSeason();

  if (sorted.length === 0) {
    return null;
  }

  return (
    <section className="group/carousel">
      <div className="mb-5 flex items-end justify-between gap-4">
        <h2 className="text-xl font-semibold">Seasons</h2>
        <span className="text-muted-foreground text-sm">{sorted.length} total</span>
      </div>

      <div className="relative">
        {canScrollPrev && (
          <button
            type="button"
            onClick={scrollPrev}
            className="from-background/90 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll left"
          >
            <ChevronLeft className="text-foreground h-6 w-6" />
          </button>
        )}

        <div ref={emblaRef} className="embla__viewport -mt-1 overflow-hidden pt-1 pb-5">
          <ul role="list" className="embla__container flex cursor-grab list-none gap-4">
            {sorted.map((season) => (
              <li key={season.content_id} className="embla__slide shrink-0">
                <SeasonCard
                  season={season}
                  parentSeriesHref={parentSeriesHref}
                  onPrefetch={() => prefetchSeason(season.content_id)}
                />
              </li>
            ))}
          </ul>
        </div>

        {canScrollNext && (
          <button
            type="button"
            onClick={scrollNext}
            className="from-background/90 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-(--duration-fast) group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll right"
          >
            <ChevronRight className="text-foreground h-6 w-6" />
          </button>
        )}
      </div>
    </section>
  );
}

function SeasonCard({
  season,
  parentSeriesHref,
  onPrefetch,
}: {
  season: Season;
  parentSeriesHref?: string;
  onPrefetch: () => void;
}) {
  const prefetchHandlers = useDwellPrefetch(onPrefetch);
  const navigationState = parentSeriesHref
    ? ({ parentSeriesHref } satisfies SeasonNavigationState)
    : undefined;
  const userData = season.user_data;
  const isCompleted = userData?.played === true;
  const hasProgress =
    !isCompleted &&
    userData != null &&
    (userData.watched_count > 0 || userData.in_progress_count > 0);
  const progressPercent =
    hasProgress && season.episode_count > 0
      ? Math.round((userData.watched_count / season.episode_count) * 100)
      : 0;

  return (
    <div className="media-card group/season w-[160px] sm:w-[170px]" {...prefetchHandlers}>
      <div className="group/media relative">
        <ViewTransitionLink
          to={`/item/${season.content_id}`}
          state={navigationState}
          className="media-card-image relative block aspect-[2/3] overflow-hidden rounded-xl"
        >
          {season.poster_url ? (
            <img
              src={season.poster_url}
              alt={getSeasonDisplayTitle(season)}
              className="h-full w-full object-cover"
              loading="lazy"
              decoding="async"
            />
          ) : (
            <div className="text-muted-foreground bg-surface flex h-full items-center justify-center p-4 text-center text-sm font-medium">
              {getSeasonDisplayTitle(season)}
            </div>
          )}

          {isCompleted && (
            <div className="absolute top-2.5 right-2.5 rounded-full bg-green-500/90 p-1 text-white shadow-sm">
              <Check className="size-3.5" strokeWidth={3} />
            </div>
          )}

          {(isCompleted || hasProgress) && (
            <div className="absolute inset-x-2.5 bottom-2 h-[3px] overflow-hidden rounded-full bg-black/40">
              <div
                className="h-full rounded-full transition-[width] duration-(--duration-fast)"
                style={{
                  width: isCompleted ? "100%" : `${progressPercent}%`,
                  background: isCompleted ? "#4caf50" : "var(--primary)",
                }}
              />
            </div>
          )}
        </ViewTransitionLink>
        {season.play_content_id ? (
          <CardPlayOverlay
            contentId={season.play_content_id}
            title={getSeasonDisplayTitle(season)}
            type="episode"
          />
        ) : null}
      </div>

      <ViewTransitionLink
        to={`/item/${season.content_id}`}
        state={navigationState}
        className="block px-0.5 pt-2.5"
      >
        <div className="truncate text-[13px] font-semibold">{getSeasonDisplayTitle(season)}</div>
        <div className="text-muted-foreground text-xs">
          {hasProgress
            ? `${userData.watched_count} of ${season.episode_count} episodes`
            : formatSeasonMeta(season)}
        </div>
      </ViewTransitionLink>
    </div>
  );
}
