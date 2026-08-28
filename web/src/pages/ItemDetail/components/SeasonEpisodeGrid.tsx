import { useRef } from "react";
import { Play } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import { WatchedCheckIndicator } from "@/components/CardWatchedBadge";
import { toEpisodeUserState } from "@/components/episodeUserState";
import MediaCarousel from "@/components/MediaCarousel";
import MediaItemMenu from "@/components/MediaItemMenu";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import CardOverlays from "@/components/overlays/CardOverlays";
import { Skeleton } from "@/components/ui/skeleton";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { usePrefetchCatalogItemDetail } from "@/hooks/queries/catalogRead";
import { useDwellPrefetch } from "@/hooks/useDwellPrefetch";
import type { CardQuickActionMode } from "@/lib/cardQuickActions";
import { overlayDataFromEpisodeListItem, type CardOverlayPrefs } from "@/lib/overlays";
import type { EpisodeNavigationState } from "../itemDetailLayout";

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

  if (isLoading) {
    return (
      <MediaCarousel title="Episodes" edgePadding={false} showHeader={false}>
        {Array.from({ length: 5 }, (_, index) => (
          <div key={index} className="w-[260px] shrink-0 sm:w-[315px]">
            <Skeleton className="aspect-video w-full rounded-lg" />
            <Skeleton className="mt-2 h-3 w-16" />
            <Skeleton className="mt-1 h-4 w-24" />
            <Skeleton className="mt-1.5 h-3 w-20" />
          </div>
        ))}
      </MediaCarousel>
    );
  }

  if (episodes.length === 0) {
    return (
      <div className="border-border text-muted-foreground bg-surface rounded-lg border p-5 text-sm">
        No episodes are available for this season yet.
      </div>
    );
  }

  return (
    <MediaCarousel title="Episodes" edgePadding={false} showHeader={false}>
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
    </MediaCarousel>
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
    <div
      ref={cardRef}
      className="season-episode-card group/card media-card media-card-longpress w-[260px] shrink-0 sm:w-[315px]"
      {...prefetchHandlers}
    >
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
                className="h-full w-full object-cover"
                loading="lazy"
                decoding="async"
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
