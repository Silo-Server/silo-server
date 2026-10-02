import { useEffect, useRef, useState } from "react";

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
  controlsVisible?: boolean;
  focusOnMount?: boolean;
}

/**
 * The shared intro/recap pill. Intro prompts provide progress from their
 * wall-clock controller; recap retains the plain button shape for now.
 */
export function IntroSkipButton({
  onSkip,
  label = "Skip Intro",
  caption,
  timer,
  controlsVisible = true,
  focusOnMount = false,
}: IntroSkipButtonProps) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  const [now, setNow] = useState(() => Date.now());

  useEffect(() => {
    if (focusOnMount) buttonRef.current?.focus({ preventScroll: true });
  }, [focusOnMount]);

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

  const button = (
    <button
      ref={buttonRef}
      onClick={onSkip}
      type="button"
      data-intro-skip-prompt={timer === undefined ? undefined : "true"}
      className="relative min-h-11 overflow-hidden rounded-full border border-white/20 bg-black/75 px-5 py-2.5 text-sm font-semibold text-white shadow-2xl backdrop-blur-md hover:border-white/35 hover:bg-black/85 focus-visible:border-white/55 focus-visible:ring-2 focus-visible:ring-white/80 focus-visible:outline-none active:bg-white/25"
    >
      {progress !== null ? (
        <span
          aria-hidden="true"
          className="absolute inset-y-0 left-0 bg-white/15"
          style={{ width: `${Math.min(1, Math.max(0, progress)) * 100}%` }}
        />
      ) : null}
      <span className="relative">{label}</span>
    </button>
  );

  return (
    <div
      className={`animate-in fade-in absolute right-5 z-50 flex flex-col items-end gap-1.5 duration-150 sm:right-7 ${controlsVisible ? "bottom-24 sm:bottom-28" : "bottom-5 sm:bottom-7"}`}
    >
      {caption ? (
        <span className="pr-3 text-xs font-medium text-white/75 drop-shadow">{caption}</span>
      ) : null}
      {button}
    </div>
  );
}
