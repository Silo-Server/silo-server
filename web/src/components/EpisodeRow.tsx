import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Play, Star } from "lucide-react";
import type { EpisodeListItem } from "@/api/types";
import CardOverlays from "@/components/overlays/CardOverlays";
import { useOverlayPrefs } from "@/hooks/useOverlayPrefs";
import { overlayDataFromEpisodeListItem } from "@/lib/overlays";

interface EpisodeRowProps {
  episode: EpisodeListItem;
  rating?: number;
  watched?: boolean;
  progress?: number;
}

export default function EpisodeRow({ episode, rating, watched, progress }: EpisodeRowProps) {
  const { prefs: overlayPrefs } = useOverlayPrefs();
  const watchedState = watched ?? episode.user_data?.played ?? false;
  const derivedProgress =
    progress ??
    (() => {
      const position = episode.user_data?.position_seconds ?? 0;
      const duration = episode.user_data?.duration_seconds ?? 0;
      // Watched episodes store position 0; a nonzero position on a watched
      // episode is a rewatch in flight and should show its bar.
      if (duration <= 0 || position <= 0) {
        return undefined;
      }
      return Math.max(0, Math.min(100, (position / duration) * 100));
    })();

  const hasProgress = derivedProgress != null && derivedProgress > 0 && derivedProgress < 100;

  return (
    <ViewTransitionLink
      to={`/item/${episode.content_id}`}
      className={`group hover:bg-accent/60 flex items-center gap-4 rounded-lg px-3.5 py-3 transition-colors duration-150 ${
        hasProgress ? "bg-accent/5" : ""
      }`}
    >
      {/* Episode number */}
      <div className="text-muted-foreground/50 w-7 shrink-0 text-center text-sm font-semibold">
        {episode.episode_number}
      </div>

      {/* Thumbnail */}
      <div className="bg-muted/50 relative h-20 w-[140px] shrink-0 overflow-hidden rounded-lg">
        {episode.still_url ? (
          <img
            src={episode.still_url}
            alt={episode.title || `Episode ${episode.episode_number}`}
            className="h-full w-full object-cover transition-transform duration-300 group-hover:scale-[1.03]"
            loading="lazy"
          />
        ) : (
          <div className="text-muted-foreground/50 flex h-full w-full items-center justify-center">
            <Play className="size-6" />
          </div>
        )}
        {overlayPrefs && (
          <CardOverlays
            data={overlayDataFromEpisodeListItem(episode)}
            prefs={overlayPrefs}
            variant="wide"
          />
        )}
        {hasProgress && (
          <div className="bg-background/40 absolute inset-x-0 bottom-0 h-[3px]">
            <div
              className="h-full rounded-r-sm"
              style={{
                width: `${derivedProgress}%`,
                background: "var(--primary)",
              }}
            />
          </div>
        )}
      </div>

      {/* Info */}
      <div className="min-w-0 flex-1">
        <div className="mb-0.5 flex items-center gap-2">
          <span className="text-foreground truncate text-sm font-semibold">
            {episode.title || `Episode ${episode.episode_number}`}
          </span>
          {watchedState && (
            <svg
              className="text-success size-3.5 shrink-0"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              strokeWidth="3"
              strokeLinecap="round"
              strokeLinejoin="round"
            >
              <polyline points="20 6 9 17 4 12" />
            </svg>
          )}
        </div>
        <div className="text-muted-foreground text-xs">
          {episode.air_date && <span>{episode.air_date}</span>}
          {episode.air_date && episode.runtime > 0 && <span className="mx-1.5">&middot;</span>}
          {episode.runtime > 0 && <span>{episode.runtime}m</span>}
        </div>
      </div>

      {/* Rating */}
      {rating != null && (
        <div className="flex shrink-0 items-center gap-1">
          <Star className="text-primary size-3 fill-current" />
          <span className="text-primary text-[13px] font-semibold">{rating.toFixed(1)}</span>
        </div>
      )}
    </ViewTransitionLink>
  );
}
