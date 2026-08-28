// hasRouterHistory reports whether the current entry has in-app router
// history behind it, i.e. navigate(-1) stays inside the app. React Router
// stamps its entry index on window.history.state.idx; the first in-app entry
// has idx 0.
export function hasRouterHistory(): boolean {
  const historyIndex = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof historyIndex === "number" && historyIndex > 0;
}

export const MAX_TRACKED_HISTORY_PATHS = 128;

/** Keeps route provenance bounded around the entry a user can Back from. */
export function pruneNavigationHistory(paths: Map<number, string>, currentIndex: number): void {
  const firstRetainedIndex = Math.max(0, currentIndex - (MAX_TRACKED_HISTORY_PATHS - 1));
  for (const index of paths.keys()) {
    if (index < firstRetainedIndex || index > currentIndex) paths.delete(index);
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
): boolean {
  return (
    expectedSequence === currentSequence &&
    sourceLocationKey !== currentLocationKey &&
    (expectedPath === null || expectedPath === currentPath)
  );
}
