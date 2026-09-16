import { useEffect } from "react";

/**
 * Registers keyboard shortcuts for the video player.
 * Space/K = play/pause, F = fullscreen, M = mute, C = toggle captions,
 * P = picture-in-picture, ArrowLeft/Right = skip by the profile's intervals, ArrowUp/Down = volume ±5%.
 */
export function useKeyboardShortcuts(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  containerRef: React.RefObject<HTMLElement | null>,
  handlePlayPause: () => void,
  skip: { back: () => void; forward: () => void },
  toggleCaptions: () => void,
  togglePiP?: () => void,
  enabled = true,
) {
  useEffect(() => {
    if (!enabled) {
      return;
    }

    function handleKeyDown(e: KeyboardEvent) {
      if (e.defaultPrevented) return;
      // Don't intercept keys when typing in inputs.
      const target = e.target as HTMLElement;
      if (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable) {
        return;
      }

      const video = videoRef.current;
      if (!video) return;

      switch (e.key) {
        case " ":
        case "k":
        case "K":
          e.preventDefault();
          handlePlayPause();
          break;

        case "f":
        case "F":
          e.preventDefault();
          {
            const webkitVideo = video as HTMLVideoElement & {
              webkitSupportsFullscreen?: boolean;
              webkitDisplayingFullscreen?: boolean;
              webkitEnterFullscreen?: () => void;
              webkitExitFullscreen?: () => void;
            };
            if (document.fullscreenElement) {
              document.exitFullscreen().catch(() => {});
            } else if (webkitVideo.webkitDisplayingFullscreen) {
              webkitVideo.webkitExitFullscreen?.();
            } else if (containerRef.current?.requestFullscreen) {
              containerRef.current.requestFullscreen().catch(() => {
                if (
                  webkitVideo.webkitSupportsFullscreen !== false &&
                  typeof webkitVideo.webkitEnterFullscreen === "function"
                ) {
                  webkitVideo.webkitEnterFullscreen();
                }
              });
            } else if (
              webkitVideo.webkitSupportsFullscreen !== false &&
              typeof webkitVideo.webkitEnterFullscreen === "function"
            ) {
              webkitVideo.webkitEnterFullscreen();
            }
          }
          break;

        case "m":
        case "M":
          e.preventDefault();
          video.muted = !video.muted;
          break;

        case "c":
        case "C":
          e.preventDefault();
          toggleCaptions();
          break;

        case "p":
        case "P":
          e.preventDefault();
          togglePiP?.();
          break;

        case "ArrowLeft":
          e.preventDefault();
          skip.back();
          break;

        case "ArrowRight":
          e.preventDefault();
          skip.forward();
          break;

        case "ArrowUp":
          e.preventDefault();
          video.volume = Math.min(1, video.volume + 0.05);
          break;

        case "ArrowDown":
          e.preventDefault();
          video.volume = Math.max(0, video.volume - 0.05);
          break;
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [containerRef, enabled, handlePlayPause, skip, toggleCaptions, togglePiP, videoRef]);
}
