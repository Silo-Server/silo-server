// hasRouterHistory reports whether the current entry has in-app router
// history behind it, i.e. navigate(-1) stays inside the app. React Router
// stamps its entry index on window.history.state.idx; the first in-app entry
// has idx 0.
export function hasRouterHistory(): boolean {
  const historyIndex = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof historyIndex === "number" && historyIndex > 0;
}

export const MAX_TRACKED_HISTORY_PATHS = 128;
export const NAVIGATION_COMMIT_FALLBACK_MS = 2_000;
export const NAVIGATION_HISTORY_PATHS_STORAGE_KEY = "silo:navigation-history-paths:v1";

/** Keeps the closest known Back and Forward entries within a fixed budget. */
export function pruneNavigationHistory(paths: Map<number, string>, currentIndex: number): void {
  if (paths.size <= MAX_TRACKED_HISTORY_PATHS) return;

  const retainedIndices = new Set(
    [...paths.keys()]
      .sort((left, right) => {
        const distanceDifference = Math.abs(left - currentIndex) - Math.abs(right - currentIndex);
        return distanceDifference || left - right;
      })
      .slice(0, MAX_TRACKED_HISTORY_PATHS),
  );
  for (const index of paths.keys()) {
    if (!retainedIndices.has(index)) paths.delete(index);
  }
}

/** Matches only the active navigation's intended commit, never a stale route. */
export function isExpectedNavigationCommit(
  expectedPath: string | null,
  expectedSequence: number,
  currentSequence: number,
  sourceLocationKey: string,
  currentLocationKey: string,
  currentPath: string,
  acceptPathMismatch = false,
): boolean {
  return (
    expectedSequence === currentSequence &&
    sourceLocationKey !== currentLocationKey &&
    (acceptPathMismatch || expectedPath === null || expectedPath === currentPath)
  );
}
