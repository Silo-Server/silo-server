import { BookOpen, Play } from "lucide-react";
import { useCallback } from "react";
import { useLocation } from "react-router";
import type { WatchTonightItem } from "@/hooks/queries/recommendations";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Badge } from "@/components/ui/badge";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { parseWatchHref } from "@/pages/watchRouteHelpers";

const sourceLabels: Record<string, string> = {
  continue_watching: "Continue",
  next_up: "Next Up",
  recommendation: "For You",
};

interface WatchTonightCardProps {
  item: WatchTonightItem;
  onPlay: () => void;
}

export default function WatchTonightCard({ item, onPlay }: WatchTonightCardProps) {
  const location = useLocation();
  const playbackController = useWatchPlaybackController();

  const watchHref =
    item.type === "ebook"
      ? `/reader/ebook/${encodeURIComponent(item.content_id)}`
      : `/watch/${item.content_id}`;
  const itemHref = `/item/${encodeURIComponent(item.content_id)}`;
  const source = item.watch_tonight_source ?? "";
  const isInProgress = source === "continue_watching";
  const isNextUp = source === "next_up";
  const hasEpisodeMeta = item.season_number != null && item.episode_number != null;
  const heading = hasEpisodeMeta && item.series_title ? item.series_title : item.title;
  const progressPercent =
    isInProgress && (item.duration_seconds ?? 0) > 0
      ? ((item.position_seconds ?? 0) / (item.duration_seconds ?? 1)) * 100
      : 0;

  const episodeMeta = hasEpisodeMeta
    ? `S${item.season_number} E${item.episode_number}${item.title && item.series_title ? ` \u2022 ${item.title}` : ""}`
    : null;

  const subtitle = isNextUp
    ? "Next Episode"
    : isInProgress && item.type === "ebook"
      ? `${Math.round(Math.min(Math.max(progressPercent, 0), 100))}% read`
      : isInProgress && (item.duration_seconds ?? 0) > 0
        ? `${Math.max(0, Math.round(((item.duration_seconds ?? 0) - (item.position_seconds ?? 0)) / 60))} min left`
        : item.genres?.slice(0, 2).join(", ") || "\u00A0";

  const handlePlayClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>) => {
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.altKey ||
        event.ctrlKey ||
        event.shiftKey
      ) {
        return;
      }

      if (item.type === "ebook") {
        return;
      }

      const parsed = parseWatchHref(watchHref);
      if (!parsed) return;

      event.preventDefault();
      onPlay();
      playbackController.startPlayback({
        contentId: parsed.contentId,
        fileId: parsed.fileId,
        libraryId: parsed.libraryId,
        restart: parsed.restart,
        returnHref: `${location.pathname}${location.search}`,
      });
    },
    [watchHref, location.pathname, location.search, playbackController, onPlay],
  );

  const badgeLabel = sourceLabels[source];

  return (
    <div className="group/card flex gap-5 rounded-xl p-3 transition-colors hover:bg-white/5">
      {/* Thumbnail */}
      <ViewTransitionLink
        to={watchHref}
        onClick={handlePlayClick}
        aria-label={`Play ${heading}`}
        className="group/play relative w-44 shrink-0"
      >
        <div className="relative aspect-video overflow-hidden rounded-lg">
          {item.backdrop_url ? (
            <img
              src={item.backdrop_url}
              alt={heading}
              className="h-full w-full object-cover"
              loading="lazy"
            />
          ) : item.poster_url ? (
            <img
              src={item.poster_url}
              alt={heading}
              className="h-full w-full object-cover"
              loading="lazy"
            />
          ) : (
            <div className="text-muted-foreground bg-surface flex h-full w-full items-center justify-center text-xs">
              No Image
            </div>
          )}

          {/* Play overlay */}
          <div className="absolute inset-0 flex items-center justify-center bg-black/0 transition-colors group-hover/card:bg-black/30 group-focus-visible/play:bg-black/30">
            <div className="bg-primary text-primary-foreground flex h-10 w-10 items-center justify-center rounded-full opacity-0 shadow-lg transition-opacity group-hover/card:opacity-100 group-focus-visible/play:opacity-100">
              {item.type === "ebook" ? (
                <BookOpen className="h-4 w-4" />
              ) : (
                <Play className="ml-0.5 h-4 w-4" fill="currentColor" />
              )}
            </div>
          </div>

          {/* Progress bar */}
          {isInProgress && progressPercent > 0 && (
            <div className="bg-background/40 absolute inset-x-0 bottom-0 h-[3px]">
              <div
                className="h-full transition-all duration-300"
                style={{
                  width: `${Math.min(progressPercent, 100)}%`,
                  background: "var(--primary)",
                }}
              />
            </div>
          )}
        </div>
      </ViewTransitionLink>

      {/* Info */}
      <div className="flex min-w-0 flex-1 flex-col justify-center gap-1">
        <div className="flex items-center gap-2">
          {badgeLabel && (
            <Badge variant="secondary" className="shrink-0 text-[10px]">
              {badgeLabel}
            </Badge>
          )}
        </div>
        <ViewTransitionLink to={itemHref} className="min-w-0">
          <div className="truncate text-[15px] leading-tight font-semibold">{heading}</div>
        </ViewTransitionLink>
        {episodeMeta && (
          <div className="text-muted-foreground truncate text-[13px]">{episodeMeta}</div>
        )}
        <div className="text-muted-foreground text-[13px]">{subtitle}</div>
      </div>
    </div>
  );
}
