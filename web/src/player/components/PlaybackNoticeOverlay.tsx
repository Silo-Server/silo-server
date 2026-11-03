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
  const accentClass =
    tone === "warning" ? "border-amber-400/50 bg-amber-500/15" : "border-sky-400/50 bg-sky-500/15";

  return (
    <div
      className={`max-w-xl rounded-2xl border px-5 py-4 text-white shadow-2xl backdrop-blur ${accentClass}`}
    >
      {title ? <div className="text-sm font-semibold tracking-wide text-white">{title}</div> : null}
      <div className={`text-sm leading-6 text-white/85 ${title ? "mt-1" : ""}`}>{message}</div>
      {actionLabel && onAction ? (
        <button
          type="button"
          onClick={onAction}
          className="pointer-events-auto mt-3 rounded-lg bg-white/15 px-3 py-2 text-sm font-medium text-white hover:bg-white/25"
        >
          {actionLabel}
        </button>
      ) : null}
    </div>
  );
}
