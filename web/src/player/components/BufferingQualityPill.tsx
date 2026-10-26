import { useEffect, useId, useRef, useState } from "react";
import {
  BUFFERING_PILL_MS,
  BUFFERING_QUALITY_COPY as COPY,
  type BufferingAnswer,
  type BufferingPill,
} from "../buffering-quality";

interface BufferingQualityPillProps {
  kind: BufferingPill;
  remainingMs: number;
  /** When the timer runs out, or null while it is frozen. */
  deadlineMs: number | null;
  /** Whether the primary action takes focus as the pill appears. */
  focusOnMount: boolean;
  onAnswer: (answer: BufferingAnswer) => void;
}

/** The intro pill's capsule, so every timed player action reads the same way. */
const ACTION_CLASS =
  "relative min-h-11 overflow-hidden rounded-full border border-white/20 bg-black/75 px-5 py-2.5 text-sm font-semibold text-white shadow-2xl hover:border-white/35 hover:bg-black/85 focus-visible:border-white/55 focus-visible:ring-2 focus-visible:ring-white/80 focus-visible:outline-none active:bg-white/25";

/**
 * The buffering prompt, "Playback keeps buffering" with Lower quality and Keep
 * quality, or the undo, "Quality lowered to stop buffering" over Restore
 * quality. It is a notice card in the player's notice slot, and the primary
 * action fills as the wall-clock timer runs, as the intro pill does. The
 * caption and the actions are separate elements, never one phrase.
 */
export function BufferingQualityPill({
  kind,
  remainingMs,
  deadlineMs,
  focusOnMount,
  onAnswer,
}: BufferingQualityPillProps) {
  const titleId = useId();
  const primaryRef = useRef<HTMLButtonElement>(null);
  const [now, setNow] = useState(() => performance.now());
  // Decided once as the pill appears; later keyboard input must not pull
  // focus onto it.
  const [takeFocus] = useState(focusOnMount);

  useEffect(() => {
    if (takeFocus) primaryRef.current?.focus({ preventScroll: true });
  }, [takeFocus]);

  useEffect(() => {
    if (deadlineMs === null) return;
    const interval = window.setInterval(() => setNow(performance.now()), 50);
    return () => window.clearInterval(interval);
  }, [deadlineMs]);

  const left = deadlineMs === null ? remainingMs : Math.max(0, deadlineMs - now);
  const progress = Math.min(1, Math.max(0, 1 - left / BUFFERING_PILL_MS));
  const prompt = kind === "prompt";

  return (
    <div
      role="group"
      aria-labelledby={titleId}
      data-buffering-prompt="true"
      className="animate-in fade-in pointer-events-auto max-w-xl rounded-2xl border border-sky-400/50 bg-sky-500/15 px-5 py-4 text-white shadow-2xl backdrop-blur duration-150"
    >
      <div id={titleId} role="status" className="text-sm font-semibold tracking-wide text-white">
        {prompt ? COPY.prompt_title : COPY.undo_caption}
      </div>
      <div className="mt-3 flex flex-wrap gap-2">
        <button
          ref={primaryRef}
          type="button"
          className={ACTION_CLASS}
          onClick={() => onAnswer(prompt ? "select_lower" : "select_restore")}
        >
          <span
            aria-hidden="true"
            className="absolute inset-y-0 left-0 bg-white/15"
            style={{ width: `${progress * 100}%` }}
          />
          <span className="relative">{prompt ? COPY.prompt_lower : COPY.undo_action}</span>
        </button>
        {prompt ? (
          <button
            type="button"
            data-buffering-keep="true"
            className={ACTION_CLASS}
            onClick={() => onAnswer("select_keep")}
          >
            {COPY.prompt_keep}
          </button>
        ) : null}
      </div>
    </div>
  );
}
