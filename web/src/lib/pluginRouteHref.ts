// Build a navigable URL for a plugin route. The descriptor's `path` is a route
// pattern (it may end in `/*` to mark a prefix-matched range), not a URL — we
// strip the trailing wildcard before composing the installation-scoped href.
export function pluginRouteHref(installationId: number, path: string): string {
  const trimmed = path.endsWith("/*") ? path.slice(0, -2) : path;
  return `/api/v2/plugin-content/plugins/${installationId}${trimmed}`;
}
