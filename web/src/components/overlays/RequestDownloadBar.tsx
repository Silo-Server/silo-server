/**
 * The download bar a watchlist title's card draws while it downloads: the
 * Continue Watching progress bar (3px, inset, primary colour). The card's
 * caption says the same in words, so the bar is hidden from assistive tech.
 * Hosts pass hasProgressBar to <CardOverlays /> so bottom badges rise clear.
 */
export function RequestDownloadBar({ percent }: { percent: number }) {
  return (
    <div
      data-request-download-bar
      aria-hidden="true"
      className="pointer-events-none absolute inset-x-2.5 bottom-2 z-10 h-[3px] overflow-hidden rounded-full bg-black/40"
    >
      <div
        className="bg-primary h-full rounded-full transition-all duration-300"
        style={{ width: `${Math.min(100, Math.max(0, percent))}%` }}
      />
    </div>
  );
}
