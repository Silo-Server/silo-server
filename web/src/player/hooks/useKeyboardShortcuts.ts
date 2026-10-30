import { useEffect } from "react";

import { REMOTE_BACK_KEYS } from "@/hooks/useRemoteBackKey";

/** What a TV remote needs from the player beyond the desktop shortcuts. */
export interface RemoteControls {
  container: React.RefObject<HTMLElement | null>;
  /** Whether the controls are on screen, which makes focus inside them real. */
  visible: boolean;
  /** Shows the controls and restarts their hide timer. */
  reveal: () => void;
  /** Moves focus onto the seek bar, the entry to the controls. */
  focusTransport: () => void;
  /** The Back key: the same as the player's Exit button. */
  back: () => void;
}

/**
 * Registers keyboard shortcuts for the video player.
 * Space/K = play/pause, F = fullscreen, M = mute, C = toggle captions,
 * P = picture-in-picture, ArrowLeft/Right = skip by the profile's intervals.
 *
 * With `remote`, the keys also drive the player from a TV remote, which has
 * only arrows, Enter and Back. Any key shows the controls. Up and Down bring
 * focus onto them, and once a control has focus the arrows move between
 * controls (see lib/spatialNavigation) rather than seeking. Enter plays or
 * pauses when no control has focus, and Back leaves the player.
 */
export function useKeyboardShortcuts(
  videoRef: React.RefObject<HTMLVideoElement | null>,
  toggleFullscreen: () => void,
  handlePlayPause: () => void,
  skip: { back: () => void; forward: () => void },
  toggleCaptions: () => void,
  toggleMuted: () => void,
  togglePiP?: () => void,
  enabled = true,
  remote?: RemoteControls,
) {
  useEffect(() => {
    if (!enabled) {
      return;
    }

    function handleKeyDown(e: KeyboardEvent) {
      // Before anything claims the key: a control working its own keys (the
      // seek bar on Left and Right) must not have the controls hide under it.
      remote?.reveal();
      if (e.defaultPrevented) return;
      // Don't intercept keys when typing in inputs.
      const target = e.target as HTMLElement;
      if (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable) {
        return;
      }
      // An open player menu owns the keyboard: Space and Enter choose the
      // focused item instead of toggling playback.
      if (target.closest?.('[role="menu"]')) return;

      const video = videoRef.current;
      if (!video) return;
      if (e.altKey || e.ctrlKey || e.metaKey) return;

      if (remote) {
        const container = remote.container.current;
        const onControl =
          remote.visible &&
          container !== null &&
          target !== container &&
          container.contains(target);
        if (REMOTE_BACK_KEYS.has(e.key)) {
          // Escape in fullscreen is the browser leaving fullscreen, and a
          // dialog over the player takes Back for itself.
          if (e.key === "Escape" && document.fullscreenElement) return;
          if (target.closest?.('[role="dialog"], [role="alertdialog"]')) return;
          e.preventDefault();
          remote.back();
          return;
        }
        if (e.key.startsWith("Arrow")) {
          if (onControl) return;
          if (e.key === "ArrowUp" || e.key === "ArrowDown") {
            e.preventDefault();
            remote.focusTransport();
            return;
          }
        } else if (e.key === "Enter") {
          if (onControl) return;
          e.preventDefault();
          handlePlayPause();
          return;
        }
      }

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
          toggleFullscreen();
          break;

        case "m":
        case "M":
          e.preventDefault();
          // Through the player, not the element: a room seek pre-roll mutes
          // the element for itself and keeps the viewer's choice separately.
          toggleMuted();
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
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [
    enabled,
    handlePlayPause,
    skip,
    toggleCaptions,
    toggleFullscreen,
    toggleMuted,
    togglePiP,
    videoRef,
    remote,
  ]);
}
