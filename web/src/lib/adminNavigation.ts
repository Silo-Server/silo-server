import {
  Blocks,
  Bot,
  CalendarClock,
  Captions,
  Download,
  History,
  KeyRound,
  LayoutDashboard,
  LayoutPanelTop,
  Library,
  MonitorSmartphone,
  PanelsTopLeft,
  Puzzle,
  Radio,
  RefreshCw,
  ScrollText,
  Send,
  Server,
  ShieldCheck,
  SkipForward,
  SlidersHorizontal,
  Users,
  UsersRound,
  Wrench,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { PluginInstallation } from "@/api/types";
import type { SettingsSearchGroup, SettingsSearchItem } from "@/components/settings/settingsSearch";
import { ADMIN_SETTINGS_GROUPS } from "@/lib/adminSettingsSearch";
import { pluginRouteHref } from "@/lib/pluginRouteHref";

export interface AdminNavItem extends SettingsSearchItem {
  label: string;
  icon: LucideIcon;
  href: string;
  exact?: boolean;
  external?: boolean;
}

export type AdminNavGroup = SettingsSearchGroup<AdminNavItem>;

export interface AdminNavVisibility {
  policyEditorAvailable?: boolean;
}

export const ADMIN_NAV_SECTIONS: AdminNavGroup[] = [
  {
    label: "Overview",
    items: [
      {
        label: "Dashboard",
        description: "Live sessions, content health, and server activity.",
        keywords: ["overview", "stats", "health", "scan all"],
        icon: LayoutDashboard,
        href: "/admin",
        exact: true,
      },
      {
        label: "Activity",
        description: "Live streams and current playback sessions.",
        keywords: ["streams", "sessions", "now playing", "transcode"],
        icon: Radio,
        href: "/admin/activity",
      },
      {
        label: "Logs",
        description: "Server log stream and operational output.",
        keywords: ["server logs", "debug", "tail", "events"],
        icon: ScrollText,
        href: "/admin/logs",
      },
    ],
  },
  {
    label: "Content",
    items: [
      {
        label: "Libraries",
        description: "Media libraries, paths, scanning, and catalog import/export.",
        keywords: ["library", "paths", "scan", "catalog", "seed"],
        icon: Library,
        href: "/admin/libraries",
      },
      {
        label: "Collections",
        description: "Curated and smart collection management.",
        keywords: ["collection groups", "templates", "smart collections"],
        icon: LayoutPanelTop,
        href: "/admin/collections",
      },
      {
        label: "Sections",
        description: "Home and catalog section configuration.",
        keywords: ["home rows", "rails", "featured sections"],
        icon: PanelsTopLeft,
        href: "/admin/sections",
      },
      {
        label: "Requests",
        description: "User media requests and request handling.",
        keywords: ["requested media", "approvals", "overseerr"],
        icon: Send,
        href: "/admin/requests",
      },
    ],
  },
  {
    label: "Automation",
    items: [
      {
        label: "Autoscan",
        description: "Autoscan sources, queue state, and poller behavior.",
        keywords: ["scan queue", "cephfs", "polling", "matcher"],
        icon: RefreshCw,
        href: "/admin/autoscan",
      },
      {
        label: "Scheduled Tasks",
        description: "Background task schedules, runs, and job history.",
        keywords: ["tasks", "jobs", "scheduler", "sync"],
        icon: CalendarClock,
        href: "/admin/tasks",
      },
      {
        label: "Subtitles",
        description: "Downloaded subtitle records and subtitle admin tools.",
        keywords: ["captions", "subtitle downloads", "providers"],
        icon: Captions,
        href: "/admin/subtitles",
      },
      {
        label: "Markers",
        description: "Intro, recap, and credits marker history.",
        keywords: ["intro markers", "credits", "recaps", "chapters"],
        icon: SkipForward,
        href: "/admin/marker-history",
      },
      {
        label: "Recommendations",
        description: "Recommendation diagnostics, seed data, and ranking controls.",
        keywords: ["taste", "ranking", "recommendation seeds"],
        icon: Bot,
        href: "/admin/recommendations",
      },
    ],
  },
  {
    label: "Users",
    items: [
      {
        label: "Users",
        description: "Accounts, roles, profile settings, and access.",
        keywords: ["accounts", "profiles", "roles", "permissions"],
        icon: Users,
        href: "/admin/users",
      },
      {
        label: "Access Groups",
        description: "Shared access defaults: libraries, downloads, streams, permissions.",
        keywords: ["groups", "roles", "permissions", "library access", "downloads", "limits"],
        icon: UsersRound,
        href: "/admin/access-groups",
      },
      {
        label: "Devices",
        description: "Registered devices, overrides, and per-device settings.",
        keywords: ["clients", "device overrides", "sessions"],
        icon: MonitorSmartphone,
        href: "/admin/devices",
      },
      {
        label: "Playback History",
        description: "Historical playback events across users and profiles.",
        keywords: ["history", "watched", "progress", "plays"],
        icon: History,
        href: "/admin/history",
      },
      {
        label: "History Import",
        description: "Admin history import mappings and bulk import runs.",
        keywords: ["emby", "imports", "mappings", "watch history"],
        icon: Download,
        href: "/admin/history-import",
      },
    ],
  },
  {
    label: "System",
    items: [
      {
        label: "Settings",
        description: "Server-wide settings, integrations, storage, and compatibility proxies.",
        keywords: ["configuration", "server settings", "admin settings"],
        icon: SlidersHorizontal,
        href: "/admin/settings",
      },
      {
        label: "Plugins",
        description: "Plugin catalog, repositories, installs, and plugin configuration.",
        keywords: ["extensions", "plugin catalog", "repositories"],
        icon: Blocks,
        href: "/admin/plugins",
      },
      {
        label: "Policy",
        description: "OPA policy documents, vendor modules, simulations, and decision logs.",
        keywords: ["opa", "rego", "authorization", "decision log", "access policy"],
        icon: ShieldCheck,
        href: "/admin/policy",
      },
      {
        label: "Nodes",
        description: "Stream nodes and remote worker status.",
        keywords: ["stream nodes", "workers", "transcode nodes"],
        icon: Server,
        href: "/admin/nodes",
      },
      {
        label: "API Keys",
        description: "Admin API keys and tier assignment.",
        keywords: ["tokens", "keys", "access", "rate limit tier"],
        icon: KeyRound,
        href: "/admin/api-keys",
      },
      {
        label: "Maintenance",
        description: "Operational maintenance tools.",
        keywords: ["repair", "cleanup", "system maintenance"],
        icon: Wrench,
        href: "/admin/maintenance",
      },
    ],
  },
];

export function buildAdminNavSections(visibility: AdminNavVisibility = {}): AdminNavGroup[] {
  return ADMIN_NAV_SECTIONS.map((section) => ({
    ...section,
    items: section.items.filter(
      (item) => item.href !== "/admin/policy" || visibility.policyEditorAvailable === true,
    ),
  }));
}

export function buildAdminPluginNavItems(
  installations: readonly PluginInstallation[] | undefined,
): AdminNavItem[] {
  const items: AdminNavItem[] = [];

  for (const installation of installations ?? []) {
    if (!installation.enabled) continue;

    for (const route of installation.routes ?? []) {
      if (!route.navigable || route.navigation_kind !== "admin") continue;

      const label = route.navigation_label || installation.plugin_id;
      items.push({
        label,
        description: `${installation.plugin_id} plugin app.`,
        keywords: [installation.plugin_id, "plugin", "plugin app"],
        icon: Puzzle,
        href: pluginRouteHref(installation.id, route.path),
        external: true,
      });
    }
  }

  return items;
}

export function appendAdminPluginNavSection(
  sections: readonly AdminNavGroup[],
  installations: readonly PluginInstallation[] | undefined,
): AdminNavGroup[] {
  const pluginItems = buildAdminPluginNavItems(installations);

  if (!pluginItems.length) {
    return sections.map((section) => ({ ...section, items: [...section.items] }));
  }

  return [
    ...sections.map((section) => ({ ...section, items: [...section.items] })),
    { label: "Plugin Apps", items: pluginItems },
  ];
}

export function appendAdminSettingsNavSection(sections: readonly AdminNavGroup[]): AdminNavGroup[] {
  return [
    ...sections.map((section) => ({ ...section, items: [...section.items] })),
    {
      label: "Admin Settings",
      items: ADMIN_SETTINGS_GROUPS.flatMap((group) =>
        group.items.map((item) => ({
          label: item.label,
          description: item.description,
          keywords: ["admin settings", group.label, ...(item.keywords ?? [])],
          settings: item.settings,
          icon: item.icon,
          href: `/admin/settings?tab=${encodeURIComponent(item.id)}`,
        })),
      ),
    },
  ];
}

export function buildAdminCommandNavSections(
  installations: readonly PluginInstallation[] | undefined,
  visibility: AdminNavVisibility = {},
): AdminNavGroup[] {
  return appendAdminPluginNavSection(
    appendAdminSettingsNavSection(buildAdminNavSections(visibility)),
    installations,
  );
}
