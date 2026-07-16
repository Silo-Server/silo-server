import { Link } from "react-router";
import { Check, Play } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import MediaItemMenu from "@/components/MediaItemMenu";
import CardOverlays from "@/components/overlays/CardOverlays";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { overlayDataFromEpisodeListItem } from "@/lib/overlays";
import { EpisodeGridSkeleton } from "./SectionSkeletons";
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
  const { prefs: overlayPrefs } = useOverlayPrefs();

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
    <div className="grid grid-cols-1 gap-4 min-[460px]:grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5">
      {episodes.map((episode) => {
        const hasPartialProgress =
          !episode.user_data?.played &&
          (episode.user_data?.position_seconds ?? 0) > 0 &&
          (episode.user_data?.duration_seconds ?? 0) > 0;

        return (
          <div key={episode.content_id} className="group/card media-card">
            <div className="relative">
              <Link
                to={`/item/${episode.content_id}`}
                state={episodeLinkState}
                className="group block"
              >
                <div className="media-card-image relative aspect-video">
                  {episode.still_url ? (
                    <img
                      src={episode.still_url}
                      alt={episode.title || `Episode ${episode.episode_number}`}
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
                  {episode.user_data?.played && (
                    <div className="watched-badge">
                      <Check className="size-4" />
                    </div>
                  )}
                  {!episode.user_data?.played &&
                    (episode.user_data?.position_seconds ?? 0) > 0 &&
                    (episode.user_data?.duration_seconds ?? 0) > 0 && (
                      <div className="absolute inset-x-0 bottom-0 h-[3px] bg-black/40">
                        <div
                          className="progress-fill h-full rounded-r-sm"
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
              </Link>
              <MediaItemMenu
                contentId={episode.content_id}
                mediaType="episode"
                userState={
                  episode.user_data
                    ? {
                        played: episode.user_data.played,
                        is_favorite: false,
                        in_watchlist: false,
                      }
                    : undefined
                }
                variant="wide"
                showCollectionActions={false}
                hasPartialProgress={hasPartialProgress}
              />
            </div>
            <Link to={`/item/${episode.content_id}`} state={episodeLinkState} className="block">
              <p className="text-muted-foreground mt-2 text-xs">Episode {episode.episode_number}</p>
              <p className="text-foreground truncate text-sm font-semibold">
                {episode.title || `Episode ${episode.episode_number}`}
              </p>
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
            </Link>
          </div>
        );
      })}
    </div>
  );
}
