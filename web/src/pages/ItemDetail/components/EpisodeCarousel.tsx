import { useEffect, useRef } from "react";
import { Play, ChevronLeft, ChevronRight } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { WatchedCheckIndicator } from "@/components/CardWatchedBadge";
import { toEpisodeUserState } from "@/components/episodeUserState";
import { isEpisodeStill, isEpisodeUnwatched, SPOILER_IMAGE_CLASS } from "@/lib/episodeSpoilers";
import { decodeThumbhash } from "@/lib/thumbhash";
import { cn } from "@/lib/utils";
import MediaItemMenu from "@/components/MediaItemMenu";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { episodeLinkLibraryId, type EpisodeNavigationState } from "../itemDetailLayout";
import { useCarouselEmbla } from "@/hooks/useCarouselEmbla";
import { usePrefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useDwellPrefetch } from "@/hooks/useDwellPrefetch";
import { useEpisodeSpoilerPrefs } from "@/hooks/useEpisodeSpoilerPrefs";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import type { CardQuickActionMode } from "@/lib/cardQuickActions";
import type { ScrollPreservingState } from "@/hooks/useScrollRestoration";
import { buildItemHref } from "@/lib/mediaNavigation";
import { useT } from "@/i18n";
import EpisodeCardCaption from "./EpisodeCardCaption";
import WatchProgressBar from "@/components/WatchProgressBar";

interface EpisodeCarouselProps {
  episodes: EpisodeListItem[];
  currentEpisodeNumber: number;
  episodeLinkState?: EpisodeNavigationState;
  /** The page's library scope, kept on each episode link. */
  libraryId?: number;
}

export default function EpisodeCarousel({
  episodes,
  currentEpisodeNumber,
  episodeLinkState,
  libraryId,
}: EpisodeCarouselProps) {
  const t = useT();
  const currentEpisodeIndex = episodes.findIndex(
    (episode) => episode.episode_number === currentEpisodeNumber,
  );
  const prefetchScopedEpisode = usePrefetchCatalogItemDetail(libraryId);
  const prefetchEpisode = usePrefetchCatalogItemDetail();
  const { quickActionMode } = useOverlayPrefs();
  const { hideImages, hideOverviews } = useEpisodeSpoilerPrefs();
  // Moving between siblings keeps the viewer where they scrolled to: the page
  // keeps its layout, and the carousel re-centres on the new episode.
  const linkState: EpisodeNavigationState & ScrollPreservingState = {
    ...episodeLinkState,
    preserveScroll: true,
  };
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
            className="from-background/90 absolute start-0 top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-r to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100 rtl:bg-gradient-to-l"
            aria-label={t("web.detail.carousel.scrollLeft")}
          >
            <ChevronLeft className="text-foreground h-6 w-6" />
          </button>
        )}

        <div ref={emblaRef} className="embla__viewport overflow-hidden py-4 ps-4">
          <ul role="list" className="embla__container flex cursor-grab list-none gap-3">
            {episodes.map((ep) => (
              <EpisodeCarouselCard
                key={ep.content_id}
                ep={ep}
                isCurrent={ep.episode_number === currentEpisodeNumber}
                episodeLinkState={linkState}
                libraryId={libraryId}
                quickActionMode={quickActionMode}
                hideImageIfUnwatched={hideImages}
                hideOverviewIfUnwatched={hideOverviews}
                onPrefetch={() =>
                  (episodeLinkLibraryId(ep, libraryId) === undefined
                    ? prefetchEpisode
                    : prefetchScopedEpisode)(ep.content_id)
                }
              />
            ))}
          </ul>
        </div>

        {canScrollNext && (
          <button
            type="button"
            onClick={scrollNext}
            className="from-background/90 absolute end-0 top-0 bottom-0 z-10 flex h-11 w-11 items-center justify-center self-center bg-gradient-to-l to-transparent opacity-0 transition-opacity duration-200 group-hover/carousel:opacity-100 focus-visible:opacity-100 rtl:bg-gradient-to-r"
            aria-label={t("web.detail.carousel.scrollRight")}
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
  libraryId,
  quickActionMode,
  hideImageIfUnwatched,
  hideOverviewIfUnwatched,
  onPrefetch,
}: {
  ep: EpisodeListItem;
  isCurrent: boolean;
  episodeLinkState?: EpisodeNavigationState;
  libraryId?: number;
  quickActionMode: CardQuickActionMode;
  hideImageIfUnwatched: boolean;
  hideOverviewIfUnwatched: boolean;
  onPrefetch: () => void;
}) {
  const t = useT();
  const cardRef = useRef<HTMLDivElement>(null);
  const prefetchHandlers = useDwellPrefetch(onPrefetch);
  const thumbhashUrl = ep.still_thumbhash ? decodeThumbhash(ep.still_thumbhash) : "";
  const episodeTitle =
    ep.title || t("web.detail.episodes.numbered", { episode: ep.episode_number });
  const episodeHref = buildItemHref({
    contentId: ep.content_id,
    libraryId: episodeLinkLibraryId(ep, libraryId),
  });
  const hideImage =
    hideImageIfUnwatched &&
    isEpisodeStill(ep.still_is_episode_still) &&
    isEpisodeUnwatched(ep.user_data);
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
        {...prefetchHandlers}
      >
        <div className="relative">
          <ViewTransitionLink
            to={episodeHref}
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
                !hideImage && thumbhashUrl
                  ? { backgroundImage: `url(${thumbhashUrl})`, backgroundSize: "cover" }
                  : undefined
              }
            >
              {ep.still_url ? (
                <img
                  src={ep.still_url}
                  alt={episodeTitle}
                  className={cn("h-full w-full object-cover", hideImage && SPOILER_IMAGE_CLASS)}
                  loading="lazy"
                  decoding="async"
                />
              ) : (
                <div className="bg-accent/30 flex h-full w-full items-center justify-center">
                  <Play size={28} className="text-muted-foreground/30" />
                </div>
              )}
              {isCurrent && (
                <div className="bg-primary text-primary-foreground pointer-events-none absolute start-2.5 top-2.5 z-10 flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[0.625rem] leading-none font-semibold tracking-[0.08em] uppercase shadow-[0_2px_8px_rgb(0_0_0/0.28)]">
                  <span className="relative flex size-1.5">
                    <span className="bg-primary-foreground absolute inline-flex h-full w-full animate-ping rounded-full opacity-60" />
                    <span className="bg-primary-foreground relative inline-flex size-1.5 rounded-full" />
                  </span>
                  {t("web.detail.episodes.nowViewing")}
                </div>
              )}
              {progress != null && <WatchProgressBar percent={progress} />}
            </div>
          </ViewTransitionLink>
          <MediaItemMenu
            contentId={ep.content_id}
            mediaType="episode"
            userState={toEpisodeUserState(ep.user_data)}
            variant="wide"
            showCollectionActions={false}
            showWatchedShortcut
            hasPartialProgress={progress != null}
            hasResumePosition={(ep.user_data?.position_seconds ?? 0) > 0}
            quickActionMode={quickActionMode}
            longPressRef={cardRef}
            itemTitle={episodeTitle}
          />
        </div>
        <ViewTransitionLink
          to={episodeHref}
          state={episodeLinkState}
          aria-current={isCurrent ? "page" : undefined}
          className="block"
        >
          <EpisodeCardCaption
            episode={ep}
            hideOverview={hideOverviewIfUnwatched && isEpisodeUnwatched(ep.user_data)}
            isCurrent={isCurrent}
            badges={ep.user_data?.played && <WatchedCheckIndicator />}
          />
        </ViewTransitionLink>
      </div>
    </li>
  );
}
