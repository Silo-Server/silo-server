import { useCallback } from "react";
import { Play } from "lucide-react";
import { useLocation } from "react-router";

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
      playbackController.startPlayback({
        contentId: parsed.contentId,
        fileId: parsed.fileId,
        libraryId: parsed.libraryId,
        restart: parsed.restart,
        returnHref: `${location.pathname}${location.search}`,
      });
    },
    [location.pathname, location.search, onPlaybackStart, playbackController, watchHref],
  );

  return (
    <ViewTransitionLink
      to={watchHref}
      onClick={handleClick}
      aria-label={`Play ${title}`}
      className={cn(
        // media-card-play-trigger owns the hover/focus reveal so every card
        // surface shares one rule (app.css): a direct :hover selector gated on
        // any-hover/any-pointer, which Tailwind's group-hover variant cannot
        // express correctly on hybrid devices.
        "media-card-play-trigger bg-primary text-primary-foreground absolute top-1/2 left-1/2 z-10 flex -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-full shadow-lg transition-all duration-200 hover:scale-110 hover:shadow-xl hover:brightness-110 active:scale-95",
        size === "compact" ? "h-6 w-6" : "h-9 w-9",
      )}
    >
      <Play
        className={cn("ml-px", size === "compact" ? "h-2.5 w-2.5" : "h-[15px] w-[15px]")}
        fill="currentColor"
      />
    </ViewTransitionLink>
  );
}
