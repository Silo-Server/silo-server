import { useCallback } from "react";
import { Play } from "lucide-react";
import { useLocation } from "react-router";

import { MEDIA_CARD_CENTER_ACTION_CLASS } from "@/components/MediaCardArtwork";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { buildMediaPlayHref, isVideoWatchHref } from "@/lib/mediaNavigation";
import { parseWatchHref } from "@/pages/watchRouteHelpers";
import { useWatchPlaybackController } from "@/playback/watchPlaybackContext";
import { cn } from "@/lib/utils";

interface CardPlayOverlayProps {
  contentId: string;
  title: string;
  type?: "movie" | "episode";
  libraryId?: number;
  size?: "standard" | "compact";
  onPlaybackStart?: () => void;
}

export default function CardPlayOverlay({
  contentId,
  title,
  type: mediaType = "episode",
  libraryId,
  size = "standard",
  onPlaybackStart,
}: CardPlayOverlayProps) {
  const location = useLocation();
  const playbackController = useWatchPlaybackController();
  const watchHref = buildMediaPlayHref({ contentId, type: mediaType, libraryId });

  const handleClick = useCallback(
    (event: React.MouseEvent<HTMLAnchorElement>) => {
      event.stopPropagation();
      if (
        event.defaultPrevented ||
        event.button !== 0 ||
        event.metaKey ||
        event.altKey ||
        event.ctrlKey ||
        event.shiftKey ||
        !isVideoWatchHref(watchHref)
      ) {
        return;
      }

      const parsed = parseWatchHref(watchHref);
      if (!parsed) return;

      event.preventDefault();
      onPlaybackStart?.();
      playbackController.startPlayback(
        {
          contentId: parsed.contentId,
          fileId: parsed.fileId,
          libraryId: parsed.libraryId,
          restart: parsed.restart,
          returnHref: `${location.pathname}${location.search}`,
        },
        "viewer",
      );
    },
    [location.pathname, location.search, onPlaybackStart, playbackController, watchHref],
  );

  return (
    <ViewTransitionLink
      to={watchHref}
      onClick={handleClick}
      aria-label={`Play ${title}`}
      className={cn(MEDIA_CARD_CENTER_ACTION_CLASS, size === "compact" ? "h-6 w-6" : "h-9 w-9")}
    >
      <Play
        className={cn("ml-px", size === "compact" ? "h-2.5 w-2.5" : "h-[15px] w-[15px]")}
        fill="currentColor"
      />
    </ViewTransitionLink>
  );
}
