/**
 * Media the app plays outside the document, such as detail-page theme music.
 * Its events never reach a document listener, so code that needs to know
 * whether any media plays (the connection speed test) asks here as well.
 * Kept free of other imports so a page can register its element without
 * loading the player.
 */
const tracked = new Set<HTMLMediaElement>();
const listeners = new Set<() => void>();

/** The events after which a tracked element may have started or stopped. */
const MEDIA_EVENTS = ["play", "playing", "pause", "ended", "emptied"] as const;

function notify(): void {
  for (const listener of listeners) listener();
}

/**
 * Counts `element` as media until the returned function runs. Call that once
 * the element is cleared, so a stopped element is not held forever.
 */
export function trackDetachedMedia(element: HTMLMediaElement): () => void {
  tracked.add(element);
  for (const type of MEDIA_EVENTS) element.addEventListener(type, notify);
  return () => {
    if (!tracked.delete(element)) return;
    for (const type of MEDIA_EVENTS) element.removeEventListener(type, notify);
    notify();
  };
}

/** Whether a tracked element is playing or has been asked to play. */
export function detachedMediaPlaying(): boolean {
  for (const element of tracked) if (!element.paused && !element.ended) return true;
  return false;
}

/** Runs `listener` whenever a tracked element may have started or stopped. */
export function onDetachedMediaChange(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}
