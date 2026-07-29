import type { CSSProperties } from "react";

export function isSidebarExpanded(collapsed: boolean, hovered: boolean, profileMenuOpen: boolean) {
  return !collapsed || hovered || profileMenuOpen;
}

export function getProfileMenuSide(collapsed: boolean) {
  return collapsed ? "right" : "top";
}

/** Visible width of the collapsed rail, in px. */
export const SIDEBAR_RAIL_WIDTH = 64;

/** Physical width of the sidebar surface in every state, in px. */
export const SIDEBAR_SURFACE_WIDTH = 260;

/**
 * True when the surface should be reduced to the rail.
 *
 * The `<aside>` is always {@link SIDEBAR_SURFACE_WIDTH} wide — nothing about it
 * reflows between states. This drives `data-collapsed`, which app.css turns into
 * paired transform animation, so the only things that move are compositor
 * layers over a surface whose 40px backdrop blur has fixed geometry.
 */
export function isSidebarRailCollapsed(collapsed: boolean, sidebarExpanded: boolean) {
  return collapsed && !sidebarExpanded;
}

/**
 * How far a library row slides left once the surface is clipped to the rail, so
 * its icon lines up with the rest of the icon column.
 *
 * The slot reserved ahead of the icon is a chevron button when the library has
 * pins — 12px padding + a 14px glyph + 4px = 30px — and a 12px border-box
 * spacer otherwise (`w-3` includes its own `pl-3` under Tailwind's preflight).
 * Every other nav row starts its icon 12px into the row, so only the chevron
 * case is out of line; the spacer already matches and must not be shifted.
 */
export function libraryRowShift(hasPins: boolean) {
  return hasPins ? "-18px" : "0px";
}

/**
 * Inline style for the sidebar surface.
 *
 * While hover-expanded on a detail page the sidebar floats over the page as an
 * overlay (raised z-index + drop shadow) so expanding it never moves or resizes
 * the main content, which stays pinned to the collapsed 64px offset.
 */
export function sidebarSurfaceStyle({
  collapsed,
  sidebarExpanded,
}: {
  collapsed: boolean;
  sidebarExpanded: boolean;
}): CSSProperties | undefined {
  if (!collapsed || !sidebarExpanded) return undefined;
  return { zIndex: 45, boxShadow: "0 25px 50px -12px rgb(0 0 0 / 0.5)" };
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
