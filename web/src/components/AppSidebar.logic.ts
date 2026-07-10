export function isSidebarExpanded(collapsed: boolean, hovered: boolean, profileMenuOpen: boolean) {
  return !collapsed || hovered || profileMenuOpen;
}

export function getProfileMenuSide(collapsed: boolean) {
  return collapsed ? "right" : "top";
}

export interface AppNavLink {
  id: string;
  basePath: string;
  label: string;
  pluginId: string;
  /** Slash-delimited category path from the plugin manifest, if any. */
  category?: string;
}

export interface AppNavGroup {
  /** Display label for the group ("Other" for uncategorized plugins). */
  category: string;
  links: AppNavLink[];
}

/** Group label for plugins whose manifest declares no category. */
export const UNCATEGORIZED_APP_GROUP = "Other";

/**
 * Groups Apps sidebar entries by the FIRST segment of the plugin manifest's
 * slash-delimited `category` path.
 *
 * SDK contract (silo-plugin-sdk proto/silo/plugin/v1/common.proto,
 * PluginManifest.category): a slash-delimited path that groups plugins in
 * the user-facing Apps section — e.g. "Tools/Utilities" lands in
 * Apps → Tools → Utilities. Plugins without a category render under
 * "Other". The host does not validate the value; the sidebar tolerates
 * unknown segments.
 *
 * We currently render only ONE level of grouping, so deeper segments
 * ("Utilities" in the example above) are intentionally ignored for now.
 *
 * Returns null when fewer than 2 distinct categories exist among the
 * links; the caller should then keep the flat list under the single
 * "Apps" header instead of rendering per-category sub-headers.
 *
 * Group ordering: categories alphabetically (locale-aware), with "Other"
 * always last. Link order within each group preserves the input order.
 */
export function groupAppNavLinks(links: AppNavLink[]): AppNavGroup[] | null {
  const groups = new Map<string, AppNavLink[]>();
  for (const link of links) {
    const firstSegment = link.category?.split("/")[0]?.trim();
    const category = firstSegment || UNCATEGORIZED_APP_GROUP;
    const bucket = groups.get(category);
    if (bucket) {
      bucket.push(link);
    } else {
      groups.set(category, [link]);
    }
  }
  if (groups.size < 2) return null;
  return [...groups.entries()]
    .sort(([a], [b]) => {
      if (a === UNCATEGORIZED_APP_GROUP) return 1;
      if (b === UNCATEGORIZED_APP_GROUP) return -1;
      return a.localeCompare(b);
    })
    .map(([category, groupLinks]) => ({ category, links: groupLinks }));
}
