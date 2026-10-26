import { AlertTriangle, CheckCircle2, Info, X } from "lucide-react";
import type { SubtitleSyncNotice } from "../hooks/useSubtitleSyncFeedback";

interface SubtitleSyncIndicatorProps {
  notice: SubtitleSyncNotice;
  onDismiss: () => void;
}

const TONE_CLASS: Record<SubtitleSyncNotice["tone"], string> = {
  progress: "border-white/15",
  success: "border-emerald-400/40",
  info: "border-sky-400/40",
  warning: "border-amber-400/50",
};

/**
 * A small card in the player's top-right corner that follows a subtitle sync:
 * its progress while it runs, then how it ended. It stays visible in
 * fullscreen, where the app's toasts do not reach.
 */
export function SubtitleSyncIndicator({ notice, onDismiss }: SubtitleSyncIndicatorProps) {
  const running = notice.tone === "progress";
  return (
    <div className="pointer-events-none absolute top-[max(1rem,env(safe-area-inset-top))] right-[max(1rem,env(safe-area-inset-right))] z-40 flex justify-end">
      <div
        role="status"
        aria-live="polite"
        data-testid="subtitle-sync-indicator"
        data-tone={notice.tone}
        className={`pointer-events-auto flex w-72 max-w-[calc(100vw-2rem)] items-start gap-3 rounded-xl border bg-black/80 px-4 py-3 text-white shadow-lg backdrop-blur ${TONE_CLASS[notice.tone]}`}
      >
        <span className="mt-0.5 shrink-0" aria-hidden="true">
          {running ? (
            <span className="block h-4 w-4 animate-spin rounded-full border-2 border-white/30 border-t-white" />
          ) : notice.tone === "success" ? (
            <CheckCircle2 className="h-4 w-4 text-emerald-300" />
          ) : notice.tone === "warning" ? (
            <AlertTriangle className="h-4 w-4 text-amber-300" />
          ) : (
            <Info className="h-4 w-4 text-sky-300" />
          )}
        </span>
        <span className="flex min-w-0 flex-1 flex-col gap-1">
          <span className="text-sm font-medium">{notice.title}</span>
          {notice.detail && <span className="text-xs text-white/70">{notice.detail}</span>}
          {running && notice.percent !== undefined && (
            <span className="mt-1 flex items-center gap-2">
              <span
                role="progressbar"
                aria-label="Subtitle sync progress"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={notice.percent}
                className="h-1 flex-1 overflow-hidden rounded-full bg-white/15"
              >
                <span
                  className="block h-full rounded-full bg-white/80 transition-[width] duration-500"
                  style={{ width: `${notice.percent}%` }}
                />
              </span>
              <span className="w-9 text-right text-[11px] text-white/60 tabular-nums">
                {notice.percent}%
              </span>
            </span>
          )}
        </span>
        {!running && (
          <button
            type="button"
            onClick={onDismiss}
            aria-label="Dismiss"
            className="-mt-1 -mr-2 shrink-0 rounded p-1 text-white/50 hover:bg-white/10 hover:text-white focus-visible:ring-2 focus-visible:ring-white/70 focus-visible:outline-none"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
    </div>
  );
}
