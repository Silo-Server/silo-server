import { useRef } from "react";
import { Play } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { WatchedCheckIndicator } from "@/components/CardWatchedBadge";
import { toEpisodeUserState } from "@/components/episodeUserState";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { usePrefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useDwellPrefetch } from "@/hooks/useDwellPrefetch";
import { useGridRowCap } from "@/hooks/useGridRowCap";
import type { CardQuickActionMode } from "@/lib/cardQuickActions";
import { overlayDataFromEpisodeListItem, type CardOverlayPrefs } from "@/lib/overlays";
import { EpisodeGridSkeleton } from "./SectionSkeletons";
import type { EpisodeNavigationState } from "../itemDetailLayout";

/**
 * How much of a season stays visible before the section scrolls, so a long one
 * cannot push the cast and crew off the page.
 *
 * Flat across breakpoints rather than scaled by column count: the cap is
 * really a height budget, and four rows is already about one screen tall on a
 * phone. Trading rows for columns there would only produce a nested scroll
 * region taller than the viewport it sits in.
 */
const VISIBLE_EPISODE_ROWS = 4;

interface SeasonEpisodeGridProps {
  episodes: EpisodeListItem[];
  isLoading: boolean;
  episodeLinkState?: EpisodeNavigationState;
}

export default function SeasonEpisodeGrid({
  episodes,
  isLoading,
  episodeLinkState,
}: SeasonEpisodeGridProps) {
  const { prefs: overlayPrefs, quickActionMode } = useOverlayPrefs();
  const prefetchEpisodeDetail = usePrefetchCatalogItemDetail();
  const setGridRef = useGridRowCap<HTMLDivElement>(VISIBLE_EPISODE_ROWS, episodes.length);

  if (isLoading) {
    return <EpisodeGridSkeleton />;
  }

  if (episodes.length === 0) {
    return (
      <div className="border-border text-muted-foreground bg-surface rounded-lg border p-5 text-sm">
        No episodes are available for this season yet.
      </div>
    );
  }

  return (
    <div
      ref={setGridRef}
      // `pt-1 -mt-1` gives the top row's 4px hover lift somewhere to go: the
      // scrollport clips both axes, and the cap adds this padding back.
      className="overlay-scroll -mt-1 grid grid-cols-2 gap-4 overflow-y-auto pt-1 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5"
    >
      {episodes.map((episode) => (
        <SeasonEpisodeCard
          key={episode.content_id}
          episode={episode}
          episodeLinkState={episodeLinkState}
          overlayPrefs={overlayPrefs}
          quickActionMode={quickActionMode}
          onPrefetch={() => prefetchEpisodeDetail(episode.content_id)}
        />
      ))}
    </div>
  );
}

function SeasonEpisodeCard({
  episode,
  episodeLinkState,
  overlayPrefs,
  quickActionMode,
  onPrefetch,
}: {
  episode: EpisodeListItem;
  episodeLinkState?: EpisodeNavigationState;
  overlayPrefs: CardOverlayPrefs | null;
  quickActionMode: CardQuickActionMode;
  onPrefetch: () => void;
}) {
  const cardRef = useRef<HTMLDivElement>(null);
  const prefetchHandlers = useDwellPrefetch(onPrefetch);
  const hasPartialProgress =
    !episode.user_data?.played &&
    (episode.user_data?.position_seconds ?? 0) > 0 &&
    (episode.user_data?.duration_seconds ?? 0) > 0;
  const episodeTitle = episode.title || `Episode ${episode.episode_number}`;

  return (
    <div ref={cardRef} className="group/card media-card media-card-longpress" {...prefetchHandlers}>
      <div className="relative">
        <ViewTransitionLink
          to={`/item/${episode.content_id}`}
          state={episodeLinkState}
          className="group block"
        >
          <div className="media-card-image relative aspect-video">
            {episode.still_url ? (
              <img
                src={episode.still_url}
                alt={episodeTitle}
                decoding="async"
                className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]"
                loading="lazy"
              />
            ) : (
              <div className="flex h-full w-full items-center justify-center">
                <Play size={32} className="text-muted-foreground/30" />
              </div>
            )}
            {overlayPrefs && (
              <CardOverlays
                data={overlayDataFromEpisodeListItem(episode)}
                prefs={overlayPrefs}
                variant="wide"
              />
            )}
            {hasPartialProgress && (
              <div className="absolute inset-x-2 bottom-1.5 h-[3px] overflow-hidden rounded-full bg-black/40">
                <div
                  className="progress-fill h-full rounded-full"
                  style={{
                    width: `${Math.max(
                      0,
                      Math.min(
                        100,
                        ((episode.user_data?.position_seconds ?? 0) /
                          (episode.user_data?.duration_seconds ?? 1)) *
                          100,
                      ),
                    )}%`,
                    background: "var(--primary)",
                  }}
                />
              </div>
            )}
          </div>
        </ViewTransitionLink>
        <MediaItemMenu
          contentId={episode.content_id}
          mediaType="episode"
          userState={toEpisodeUserState(episode.user_data)}
          variant="wide"
          showCollectionActions={false}
          showWatchedShortcut
          hasPartialProgress={hasPartialProgress}
          quickActionMode={quickActionMode}
          longPressRef={cardRef}
          itemTitle={episodeTitle}
        />
      </div>
      <ViewTransitionLink
        to={`/item/${episode.content_id}`}
        state={episodeLinkState}
        className="block"
      >
        <div className="text-muted-foreground mt-2 flex items-center gap-2 text-xs">
          <span>Episode {episode.episode_number}</span>
          {episode.user_data?.played && <WatchedCheckIndicator className="ml-auto" />}
        </div>
        <p className="text-foreground truncate text-sm font-semibold">{episodeTitle}</p>
        <div className="mt-1.5 space-y-1">
          <div className="text-muted-foreground flex items-center gap-2 text-xs">
            {episode.runtime > 0 && <span>{episode.runtime}m</span>}
            {episode.air_date && (
              <span>
                {new Intl.DateTimeFormat(undefined, {
                  month: "short",
                  day: "numeric",
                  year: "numeric",
                }).format(new Date(episode.air_date))}
              </span>
            )}
          </div>
          {episode.overview && (
            <p className="text-muted-foreground line-clamp-2 text-xs leading-relaxed">
              {episode.overview}
            </p>
          )}
        </div>
      </ViewTransitionLink>
    </div>
  );
}
