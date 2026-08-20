// hasRouterHistory reports whether the current entry has in-app router
// history behind it, i.e. navigate(-1) stays inside the app. React Router
// stamps its entry index on window.history.state.idx; the first in-app entry
// has idx 0.
export function hasRouterHistory(): boolean {
  const historyIndex = (window.history.state as { idx?: unknown } | null)?.idx;
  return typeof historyIndex === "number" && historyIndex > 0;
}
