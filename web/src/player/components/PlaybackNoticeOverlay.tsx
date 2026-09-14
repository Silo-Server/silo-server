import { useEffect, useState } from "react";

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
  const [visible, setVisible] = useState(true);

  useEffect(() => {
    setVisible(true);
    const timer = setTimeout(() => setVisible(false), 8000);
    return () => clearTimeout(timer);
  }, [title, message, onAction]);

  if (!visible) return null;

  const accentClass =
    tone === "warning" ? "border-amber-400/50 bg-amber-500/15" : "border-sky-400/50 bg-sky-500/15";

  return (
    <div className="pointer-events-none absolute inset-x-0 top-20 z-40 flex justify-center px-4">
      <div
        className={`max-w-xl rounded-2xl border px-5 py-4 text-white shadow-2xl backdrop-blur ${accentClass}`}
      >
        {title ? (
          <div className="text-sm font-semibold tracking-wide text-white">{title}</div>
        ) : null}
        <div className="mt-1 text-sm leading-6 text-white/85">{message}</div>
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
    </div>
  );
}
