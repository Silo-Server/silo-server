import { useContext, useEffect } from "react";
import { WatchPlaybackControllerContext } from "@/playback/watchPlaybackContext";
import type { AudiobookPlayback } from "./useAudiobookPlayback";
import { AUDIOBOOK_RATE_STEP, type AudiobookPrefs } from "./useAudiobookPrefs";

interface AudiobookKeyboardShortcutsOptions {
  playback: AudiobookPlayback;
  prefs: AudiobookPrefs;
  /** Whether the Now Listening full-screen view is open. */
  expanded: boolean;
  onToggleExpanded: () => void;
  onCollapse: () => void;
}

/**
 * Global keyboard shortcuts for the audiobook player, active on every route
 * while the player is mounted:
 *
 *   Space/K play-pause · ←/→ skip · ↑/↓ volume · M mute · N/P chapter ·
 *   Shift+. / Shift+, speed · E expand/collapse · Esc collapse
 *
 * Stays inert while a video watch session exists — the video player owns the
 * same keys and the audiobook bar is backgrounded in that case.
 */
export function useAudiobookKeyboardShortcuts({
  playback,
  prefs,
  expanded,
  onToggleExpanded,
  onCollapse,
}: AudiobookKeyboardShortcutsOptions) {
  // Read the watch context leniently: outside the provider (tests) it is
  // simply null rather than an error.
  const watch = useContext(WatchPlaybackControllerContext);
  const videoSessionActive = watch?.state.request != null;

  const { skipBack, skipForward } = prefs;
  const {
    hasFile,
    rate,
    volume,
    muted,
    togglePlay,
    skip,
    setRate,
    setVolume,
    setMuted,
    nextChapter,
    prevChapter,
  } = playback;

  useEffect(() => {
    if (videoSessionActive || !hasFile) return;

    function handleKeyDown(e: KeyboardEvent) {
      // A focused control (seek bar, volume slider, open menu) that already
      // handled the key wins; so does anything the user is typing into.
      if (e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
      const target = e.target as HTMLElement;
      if (
        target.tagName === "INPUT" ||
        target.tagName === "TEXTAREA" ||
        target.tagName === "SELECT" ||
        target.isContentEditable
      ) {
        return;
      }
      // Space activates a focused button/link; never steal it.
      if (e.key === " " && target.closest("button, a, [role='button'], [role='slider']")) {
        return;
      }

      switch (e.key) {
        case " ":
        case "k":
        case "K":
          e.preventDefault();
          togglePlay();
          break;
        case "ArrowLeft":
          e.preventDefault();
          skip(-skipBack);
          break;
        case "ArrowRight":
          e.preventDefault();
          skip(skipForward);
          break;
        case "ArrowUp":
          e.preventDefault();
          setVolume(Math.min(1, volume + 0.05));
          if (muted) setMuted(false);
          break;
        case "ArrowDown":
          e.preventDefault();
          setVolume(Math.max(0, volume - 0.05));
          break;
        case "m":
        case "M":
          e.preventDefault();
          setMuted(!muted);
          break;
        case "n":
        case "N":
          e.preventDefault();
          nextChapter();
          break;
        case "p":
        case "P":
          e.preventDefault();
          prevChapter();
          break;
        case ">":
          e.preventDefault();
          setRate(rate + AUDIOBOOK_RATE_STEP);
          break;
        case "<":
          e.preventDefault();
          setRate(rate - AUDIOBOOK_RATE_STEP);
          break;
        case "e":
        case "E":
          e.preventDefault();
          onToggleExpanded();
          break;
        case "Escape":
          // Open popovers close themselves on Escape; only collapse the
          // full-screen view when nothing else is consuming the key.
          if (expanded && !document.querySelector("[role='menu'], [role='dialog']")) {
            onCollapse();
          }
          break;
      }
    }

    document.addEventListener("keydown", handleKeyDown);
    return () => document.removeEventListener("keydown", handleKeyDown);
  }, [
    expanded,
    hasFile,
    muted,
    nextChapter,
    onCollapse,
    onToggleExpanded,
    prevChapter,
    rate,
    setMuted,
    setRate,
    setVolume,
    skip,
    skipBack,
    skipForward,
    togglePlay,
    videoSessionActive,
    volume,
  ]);
}
