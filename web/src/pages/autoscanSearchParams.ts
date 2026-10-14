/**
 * Where Autoscan's pages live, and where links from its earlier layouts land,
 * in one place so the pages and the redirects cannot drift apart.
 *
 * Autoscan is its own sidebar topic: Activity, Sources, and Settings are
 * separate pages. Before that it was a tab on Libraries
 * (`/admin/libraries?tab=autoscan&view=…`), and before that one page with
 * `?tab=` selecting Sources, Activity, Connections, or Settings.
 */

export const AUTOSCAN_SOURCES_HREF = "/admin/autoscan";
export const AUTOSCAN_ACTIVITY_HREF = "/admin/autoscan/activity";
export const AUTOSCAN_SETTINGS_HREF = "/admin/settings/autoscan";

/** Old view names that now live on the Settings page. */
const SETTINGS_VIEWS = new Set(["connections", "settings"]);

function pageForView(view: string | null): string {
  if (view === "activity") return AUTOSCAN_ACTIVITY_HREF;
  if (view !== null && SETTINGS_VIEWS.has(view)) return AUTOSCAN_SETTINGS_HREF;
  return AUTOSCAN_SOURCES_HREF;
}

/**
 * Builds the page URL for an old view, carrying any other query parameters.
 * The Settings page takes none, so they are dropped there.
 */
function target(view: string | null, rest: URLSearchParams): string {
  const page = pageForView(view);
  const query = page === AUTOSCAN_SETTINGS_HREF ? "" : rest.toString();
  return query ? `${page}?${query}` : page;
}

/**
 * Where `/admin/autoscan?tab=…` (or `?view=…`, which the one-page layout also
 * accepted) belongs now, or null when the URL names neither and is already
 * the Sources page.
 */
export function legacyAutoscanPageTarget(search: string): string | null {
  const params = new URLSearchParams(search);
  const view = params.get("tab") ?? params.get("view");
  if (view === null) return null;
  params.delete("tab");
  params.delete("view");
  return target(view, params);
}

/**
 * Where `/admin/libraries?tab=autoscan&view=…` belongs now, or null when the
 * Libraries URL does not name the old Autoscan tab.
 */
export function legacyLibrariesAutoscanTarget(search: string): string | null {
  const params = new URLSearchParams(search);
  if (params.get("tab") !== "autoscan") return null;
  const view = params.get("view");
  params.delete("tab");
  params.delete("view");
  return target(view, params);
}
