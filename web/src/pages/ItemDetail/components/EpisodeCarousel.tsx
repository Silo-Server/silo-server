import { useEffect, useRef } from "react";
import { Link } from "react-router";
import { Play, ChevronLeft, ChevronRight } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { WatchedCheckIndicator } from "@/components/CardWatchedBadge";
import { toEpisodeUserState } from "@/components/episodeUserState";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";
import MediaItemMenu from "@/components/MediaItemMenu";
import type { EpisodeNavigationState } from "../itemDetailLayout";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { usePrefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";

interface EpisodeCarouselProps {
  episodes: EpisodeListItem[];
  currentEpisodeNumber: number;
  episodeLinkState?: EpisodeNavigationState;
}

export default function EpisodeCarousel({
  episodes,
  currentEpisodeNumber,
  episodeLinkState,
}: EpisodeCarouselProps) {
  const currentEpisodeIndex = episodes.findIndex(
    (episode) => episode.episode_number === currentEpisodeNumber,
  );
  const prefetchEpisodeDetail = usePrefetchCatalogItemDetail();
  const { emblaApi, emblaRef, canScrollPrev, canScrollNext, scrollPrev, scrollNext } =
    useCarouselEmbla({
      options: {
        slidesToScroll: 1,
      },
    });

  // Auto-center the current episode on mount / episode change
  useEffect(() => {
    if (!emblaApi || currentEpisodeIndex === -1) return;
    emblaApi.scrollTo(currentEpisodeIndex, true);
  }, [currentEpisodeIndex, emblaApi]);

  return (
    <div className="group/carousel relative">
      <div className="relative">
        {canScrollPrev && (
          <button
            type="button"
            onClick={scrollPrev}
            className="from-background/90 absolute top-0 bottom-0 left-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll left"
          >
            <ChevronLeft className="text-foreground h-6 w-6" />
          </button>
        )}

        <div ref={emblaRef} className="embla__viewport overflow-hidden py-4 pl-4">
          <ul role="list" className="embla__container flex cursor-grab list-none gap-3">
            {episodes.map((ep) => (
              <EpisodeCarouselCard
                key={ep.content_id}
                ep={ep}
                isCurrent={ep.episode_number === currentEpisodeNumber}
                episodeLinkState={episodeLinkState}
                onPrefetch={() => prefetchEpisodeDetail(ep.content_id)}
              />
            ))}
          </ul>
        </div>

        {canScrollNext && (
          <button
            type="button"
            onClick={scrollNext}
            className="from-background/90 absolute top-0 right-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100"
            aria-label="Scroll right"
          >
            <ChevronRight className="text-foreground h-6 w-6" />
          </button>
        )}
      </div>
    </div>
  );
}

function EpisodeCarouselCard({
  ep,
  isCurrent,
  episodeLinkState,
  onPrefetch,
}: {
  ep: EpisodeListItem;
  isCurrent: boolean;
  episodeLinkState?: EpisodeNavigationState;
  onPrefetch: () => void;
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  const thumbhashUrl = ep.still_thumbhash ? decodeThumbhash(ep.still_thumbhash) : "";
  const episodeTitle = ep.title || `Episode ${ep.episode_number}`;
  const progress =
    !ep.user_data?.played &&
    (ep.user_data?.position_seconds ?? 0) > 0 &&
    (ep.user_data?.duration_seconds ?? 0) > 0
      ? Math.max(
          0,
          Math.min(
            100,
            ((ep.user_data?.position_seconds ?? 0) / (ep.user_data?.duration_seconds ?? 1)) * 100,
          ),
        )
      : null;

  return (
    <li data-episode={ep.episode_number} className="embla__slide shrink-0">
      <div
        ref={cardRef}
        className="media-card-longpress group/card w-[240px]"
        onMouseEnter={onPrefetch}
        onFocus={onPrefetch}
        onTouchStart={onPrefetch}
      >
        <div className="relative">
          <Link
            to={`/item/${ep.content_id}`}
            state={episodeLinkState}
            aria-current={isCurrent ? "page" : undefined}
            className="block"
          >
            <div
              className={cn(
                "surface-panel-subtle relative aspect-video overflow-hidden rounded-[1.1rem] border transition-[border-color,box-shadow] duration-200",
                isCurrent
                  ? "border-transparent shadow-[0_0_0_2px_var(--primary)]"
                  : "border-border/30",
              )}
              style={
                thumbhashUrl
                  ? { backgroundImage: `url(${thumbhashUrl})`, backgroundSize: "cover" }
                  : undefined
              }
            >
              {ep.still_url ? (
                <img
                  src={ep.still_url}
                  alt={episodeTitle}
                  className="h-full w-full object-cover"
                  loading="lazy"
                />
              ) : (
                <div className="bg-accent/30 flex h-full w-full items-center justify-center">
                  <Play size={28} className="text-muted-foreground/30" />
                </div>
              )}
              {isCurrent && (
                <div className="bg-primary text-primary-foreground pointer-events-none absolute top-2.5 left-2.5 z-10 flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[10px] leading-none font-semibold tracking-[0.08em] uppercase shadow-[0_2px_8px_rgb(0_0_0/0.28)]">
                  <span className="relative flex size-1.5">
                    <span className="bg-primary-foreground absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" />
                    <span className="bg-primary-foreground relative inline-flex size-1.5 rounded-full" />
                  </span>
                  Now Viewing
                </div>
              )}
              {progress != null && (
                <div className="absolute inset-x-2 bottom-1.5 h-[3px] overflow-hidden rounded-full bg-black/40">
                  <div
                    className="h-full rounded-full"
                    style={{ width: `${progress}%`, background: "var(--primary)" }}
                  />
                </div>
              )}
            </div>
          </Link>
          <MediaItemMenu
            contentId={ep.content_id}
            mediaType="episode"
            userState={toEpisodeUserState(ep.user_data)}
            variant="wide"
            showCollectionActions={false}
            showWatchedShortcut
            hasPartialProgress={progress != null}
            longPressRef={cardRef}
            itemTitle={episodeTitle}
          />
        </div>
        <Link
          to={`/item/${ep.content_id}`}
          state={episodeLinkState}
          aria-current={isCurrent ? "page" : undefined}
          className="block"
        >
          <div className="text-muted-foreground/70 mt-2 flex items-center gap-2 text-xs">
            <span>Episode {ep.episode_number}</span>
            {ep.user_data?.played && <WatchedCheckIndicator className="ml-auto" />}
          </div>
          <p
            className="truncate text-sm font-semibold"
            style={isCurrent ? { color: "var(--primary)" } : undefined}
          >
            {episodeTitle}
          </p>
          {ep.runtime > 0 && <p className="text-muted-foreground/70 text-xs">{ep.runtime}m</p>}
        </Link>
      </div>
    </li>
  );
}
