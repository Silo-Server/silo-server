import "@/styles/player-hud.css";
import type { ReactNode } from "react";

/**
 * The player's notice slot, below the top bar and clear of subtitles. A notice
 * and the buffering pill can both be up, so they stack rather than overlap.
 */
export function PlaybackNoticeStack({ children }: { children: ReactNode }) {
  return (
    <div className="pointer-events-none absolute inset-x-0 top-20 z-40 flex flex-col items-center gap-3 px-4">
      {children}
    </div>
  );
}

interface PlaybackNoticeOverlayProps {
  title?: string;
  message: string;
  tone?: "info" | "warning";
  actionLabel?: string;
  onAction?: () => void;
}

export function PlaybackNoticeOverlay({
  title,
  message,
  tone = "info",
  actionLabel,
  onAction,
}: PlaybackNoticeOverlayProps) {
  // Tone shows as a coloured rule on the card's leading edge; the card itself
  // stays the HUD's glass so text contrast never depends on the tint.
  const accentClass = tone === "warning" ? "border-s-amber-300" : "border-s-sky-300";

  return (
    <div className={`hud-card max-w-xl border-s-4 px-5 py-4 ${accentClass}`}>
      {title ? <div className="text-sm font-semibold">{title}</div> : null}
      <div className={`text-sm leading-6 text-[var(--hud-ash)] ${title ? "mt-1" : ""}`}>
        {message}
      </div>
      {actionLabel && onAction ? (
        <button
          type="button"
          onClick={onAction}
          className="hud-secondary-btn pointer-events-auto mt-3"
        >
          {actionLabel}
        </button>
      ) : null}
    </div>
  );
}
