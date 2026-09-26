import { useEffect, useRef, type ReactNode } from "react";
import { PlayerFullscreenRootContext } from "@/player/context/PlayerFullscreenContext";
import { useWatchPlaybackController } from "./watchPlaybackContext";

/**
 * The element the player makes fullscreen instead of its own container.
 * Removing the fullscreen element from the document ends fullscreen, and the
 * next episode's player cannot re-enter it without a user gesture, so this
 * element stays mounted while the player and the loading screens between
 * episodes come and go. Leaving the full player (exit, minimize,
 * picture-in-picture) leaves fullscreen with it.
 */
export function PlaybackFullscreenRoot({ children }: { children: ReactNode }) {
  const { state } = useWatchPlaybackController();
  const showsFullPlayer =
    state.request != null && (state.mode === "foreground" || state.mode === "post-roll");
  const rootRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (showsFullPlayer) return;
    const exitRootFullscreen = () => {
      if (rootRef.current && document.fullscreenElement === rootRef.current) {
        document.exitFullscreen().catch(() => {});
      }
    };
    exitRootFullscreen();
    // A fullscreen request still pending when playback left the full player
    // completes afterwards.
    document.addEventListener("fullscreenchange", exitRootFullscreen);
    return () => document.removeEventListener("fullscreenchange", exitRootFullscreen);
  }, [showsFullPlayer]);

  return (
    <div ref={rootRef}>
      <PlayerFullscreenRootContext.Provider value={rootRef}>
        {children}
      </PlayerFullscreenRootContext.Provider>
    </div>
  );
}
