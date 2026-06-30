import type { AdminNavGroup, AdminNavItem } from "@/lib/adminNavigation";

export const ADMIN_ROUTE_ACTIONS = {
  dashboard: ["server.view"],
  activity: ["server.view"],
  logs: ["logs.view"],
  libraries: ["libraries.manage"],
  maintenance: ["server.configure"],
  collections: ["libraries.manage"],
  sections: ["libraries.manage"],
  requests: ["requests.approve"],
  autoscan: ["libraries.manage"],
  history: ["server.view"],
  markerHistory: ["metadata.curate", "markers.edit"],
  historyImport: ["server.configure"],
  accessGroups: ["security.manage"],
  users: ["users.view"],
  devices: ["users.view"],
  nodes: ["nodes.view", "nodes.manage"],
  plugins: ["plugins.view", "plugins.manage"],
  settings: ["server.configure"],
  recommendations: ["recommendations.view", "recommendations.manage"],
  apiKeys: ["server.configure"],
  subtitles: ["server.configure"],
  tasks: ["tasks.view", "tasks.run"],
} as const satisfies Record<string, readonly string[]>;

export function hasAnyAdminAction(
  actionSet: ReadonlySet<string>,
  actions: readonly string[] | undefined,
) {
  if (!actions || actions.length === 0) return true;
  return actions.some((action) => actionSet.has(action));
}

export function filterAdminNavSections(
  sections: readonly AdminNavGroup[],
  actionSet: ReadonlySet<string>,
  showAll: boolean,
): AdminNavGroup[] {
  if (showAll) {
    return sections.map((section) => ({ ...section, items: [...section.items] }));
  }

  return sections
    .map((section) => ({
      ...section,
      items: section.items.filter((item) => canUseAdminNavItem(item, actionSet)),
    }))
    .filter((section) => section.items.length > 0);
}

export function canUseAdminNavItem(item: AdminNavItem, actionSet: ReadonlySet<string>) {
  return hasAnyAdminAction(actionSet, actionsForAdminHref(item.href));
}

export function firstAllowedAdminPath(actionSet: ReadonlySet<string>) {
  const ordered: Array<{ href: string; actions: readonly string[] }> = [
    { href: "/admin", actions: ADMIN_ROUTE_ACTIONS.dashboard },
    { href: "/admin/users", actions: ADMIN_ROUTE_ACTIONS.users },
    { href: "/admin/requests", actions: ADMIN_ROUTE_ACTIONS.requests },
    { href: "/admin/logs", actions: ADMIN_ROUTE_ACTIONS.logs },
    { href: "/admin/tasks", actions: ADMIN_ROUTE_ACTIONS.tasks },
    { href: "/admin/access-groups", actions: ADMIN_ROUTE_ACTIONS.accessGroups },
    { href: "/admin/settings", actions: ADMIN_ROUTE_ACTIONS.settings },
  ];
  return ordered.find((item) => hasAnyAdminAction(actionSet, item.actions))?.href ?? "/";
}

function actionsForAdminHref(href: string) {
  if (href === "/admin") return ADMIN_ROUTE_ACTIONS.dashboard;
  if (href.startsWith("/admin/activity")) return ADMIN_ROUTE_ACTIONS.activity;
  if (href.startsWith("/admin/logs")) return ADMIN_ROUTE_ACTIONS.logs;
  if (href.startsWith("/admin/libraries")) return ADMIN_ROUTE_ACTIONS.libraries;
  if (href.startsWith("/admin/maintenance")) return ADMIN_ROUTE_ACTIONS.maintenance;
  if (href.startsWith("/admin/collections")) return ADMIN_ROUTE_ACTIONS.collections;
  if (href.startsWith("/admin/sections")) return ADMIN_ROUTE_ACTIONS.sections;
  if (href.startsWith("/admin/requests")) return ADMIN_ROUTE_ACTIONS.requests;
  if (href.startsWith("/admin/autoscan")) return ADMIN_ROUTE_ACTIONS.autoscan;
  if (href.startsWith("/admin/history-import")) return ADMIN_ROUTE_ACTIONS.historyImport;
  if (href.startsWith("/admin/history")) return ADMIN_ROUTE_ACTIONS.history;
  if (href.startsWith("/admin/marker-history")) return ADMIN_ROUTE_ACTIONS.markerHistory;
  if (href.startsWith("/admin/access-groups")) return ADMIN_ROUTE_ACTIONS.accessGroups;
  if (href.startsWith("/admin/users")) return ADMIN_ROUTE_ACTIONS.users;
  if (href.startsWith("/admin/devices")) return ADMIN_ROUTE_ACTIONS.devices;
  if (href.startsWith("/admin/nodes")) return ADMIN_ROUTE_ACTIONS.nodes;
  if (href.startsWith("/admin/plugins")) return ADMIN_ROUTE_ACTIONS.plugins;
  if (href.startsWith("/admin/settings")) return ADMIN_ROUTE_ACTIONS.settings;
  if (href.startsWith("/admin/recommendations")) return ADMIN_ROUTE_ACTIONS.recommendations;
  if (href.startsWith("/admin/api-keys")) return ADMIN_ROUTE_ACTIONS.apiKeys;
  if (href.startsWith("/admin/subtitles")) return ADMIN_ROUTE_ACTIONS.subtitles;
  if (href.startsWith("/admin/tasks")) return ADMIN_ROUTE_ACTIONS.tasks;
  return undefined;
}
