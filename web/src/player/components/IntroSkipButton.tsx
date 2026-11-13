import { useEffect, useLayoutEffect, useRef, useState } from "react";

import { SkipForward } from "lucide-react";
import "@/styles/player-hud.css";
import { useT } from "@/i18n";
import { markerProvenanceLabel } from "@/lib/markerProvenance";
import type { PlayerMarkerProvenance } from "../types";

interface IntroSkipButtonProps {
  onSkip: () => void;
  label?: string;
  /**
   * Confirmation shown in a muted line above the pill, outside the button —
   * the `always` undo's "Intro skipped". Kept separate from [label] so the
   * confirmation and the action never read as one instruction.
   */
  caption?: string;
  timer?: { durationMs: number; deadlineMs: number | null; remainingMs: number };
  /** Marks the pill as the intro prompt the player's root key handler acts on. */
  introPrompt?: boolean;
  controlsVisible?: boolean;
  /** Height of the control dock; with controls up the pill sits just above it. */
  dockHeight?: number;
  focusOnMount?: boolean;
  /**
   * Where the marked range came from. Rendered as a muted line above the pill
   * when one of the provenance settings is on; absent — or unrated with only
   * the confidence setting on — draws nothing, so the pill keeps the shape it
   * had before these settings existed.
   */
  provenance?: PlayerMarkerProvenance | null;
  showConfidence?: boolean;
  showSource?: boolean;
}

/**
 * The shared intro/recap pill. Intro prompts provide progress from their
 * wall-clock controller; recap retains the plain button shape for now.
 */
export function IntroSkipButton({
  onSkip,
  label,
  caption,
  timer,
  introPrompt = false,
  controlsVisible = true,
  dockHeight = 0,
  focusOnMount = false,
  provenance,
  showConfidence = false,
  showSource = false,
}: IntroSkipButtonProps) {
  const t = useT();
  const buttonRef = useRef<HTMLButtonElement>(null);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (focusOnMount) buttonRef.current?.focus({ preventScroll: true });
  }, [focusOnMount]);

  // A timed pill that runs out can stay on screen as the untimed offer. It
  // gives up the focus it held, as it did when it unmounted, so the next Space
  // reaches play/pause instead of skipping the intro.
  const timed = timer !== undefined;
  const wasTimedRef = useRef(timed);
  useLayoutEffect(() => {
    const button = buttonRef.current;
    if (wasTimedRef.current && !timed && button !== null && document.activeElement === button) {
      button.blur();
    }
    wasTimedRef.current = timed;
  }, [timed]);

  useEffect(() => {
    if (timer?.deadlineMs == null) return;
    const interval = window.setInterval(() => setNow(Date.now()), 50);
    return () => window.clearInterval(interval);
  }, [timer?.deadlineMs]);

  const timerRemaining = timer
    ? timer.deadlineMs === null
      ? timer.remainingMs
      : Math.max(0, timer.deadlineMs - now)
    : null;
  const progress =
    timer && timerRemaining !== null
      ? Math.min(1, Math.max(0, 1 - timerRemaining / timer.durationMs))
      : null;

  // Above the caption, not between it and the button: the `always` undo's
  // confirmation is deliberately adjacent to its action, and a metadata line
  // wedged in there would rebuild the two-instruction block that copy avoided.
  const provenanceLabel = provenance
    ? markerProvenanceLabel(provenance, { showConfidence, showSource })
    : null;

  const button = (
    <button
      ref={buttonRef}
      onClick={onSkip}
      type="button"
      data-intro-skip-prompt={introPrompt ? "true" : undefined}
      className="hud-glass-btn hud-glass-btn--text relative min-h-12 overflow-hidden ps-5 pe-4 text-[0.9375rem] shadow-[0_1rem_2.5rem_-1rem_rgb(0_0_0/0.8)]"
    >
      {progress !== null ? (
        <span
          aria-hidden="true"
          className="absolute inset-y-0 start-0 bg-white/20"
          style={{ width: `${Math.min(1, Math.max(0, progress)) * 100}%` }}
        />
      ) : null}
      <span className="relative">{label ?? t("introSkip.pill.skip")}</span>
      <SkipForward aria-hidden="true" className="relative" fill="currentColor" strokeWidth={0} />
    </button>
  );

  return (
    <div
      className="animate-in fade-in absolute right-[max(1rem,env(safe-area-inset-right))] z-50 flex flex-col items-end gap-1.5 transition-[bottom] duration-200 sm:right-[max(2rem,env(safe-area-inset-right))] rtl:right-auto rtl:left-[max(1rem,env(safe-area-inset-left))] sm:rtl:left-[max(2rem,env(safe-area-inset-left))]"
      style={{
        bottom:
          controlsVisible && dockHeight > 0
            ? `calc(${dockHeight}px + 0.5rem)`
            : "max(1.5rem, env(safe-area-inset-bottom))",
      }}
    >
      {provenanceLabel ? (
        <span
          data-marker-provenance="true"
          className="hud-card rounded-full px-3 py-1 text-xs font-medium"
        >
          {provenanceLabel}
        </span>
      ) : null}
      {caption ? (
        <span className="hud-card rounded-full px-3 py-1 text-xs font-medium">{caption}</span>
      ) : null}
      {button}
    </div>
  );
}
