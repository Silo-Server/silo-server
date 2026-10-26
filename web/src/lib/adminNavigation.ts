import {
  Blocks,
  Bot,
  CalendarClock,
  Captions,
  Download,
  FileWarning,
  HardDriveDownload,
  History,
  KeyRound,
  LayoutDashboard,
  LayoutPanelTop,
  Library,
  MonitorSmartphone,
  PlayCircle,
  Puzzle,
  Radar,
  Radio,
  Rows2,
  ScrollText,
  Send,
  Server,
  Settings2,
  Shield,
  ShieldCheck,
  SkipForward,
  Users,
  UsersRound,
  Wrench,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

import type { PluginInstallation } from "@/api/types";
import type { SettingsSearchGroup, SettingsSearchItem } from "@/components/settings/settingsSearch";
import { ADMIN_SETTINGS_NAV, adminSettingsHref } from "@/lib/adminSettingsSearch";
import { pluginRouteHref } from "@/lib/pluginRouteHref";

export interface AdminNavItem extends SettingsSearchItem {
  label: string;
  icon: LucideIcon;
  href: string;
  exact?: boolean;
  external?: boolean;
}

/** A sidebar topic: a heading that opens to list its pages. */
export interface AdminNavTopic {
  /** Stable key for the topic's remembered open state. */
  id: string;
  label: string;
  icon: LucideIcon;
  items: AdminNavItem[];
}

export type AdminNavEntry = AdminNavItem | AdminNavTopic;

export interface AdminNavSection {
  label: string;
  entries: AdminNavEntry[];
}

/** A flat group of pages, as the command palette lists them. */
export type AdminNavGroup = SettingsSearchGroup<AdminNavItem>;

export interface AdminNavVisibility {
  policyEditorAvailable?: boolean;
}

export function isAdminNavTopic(entry: AdminNavEntry): entry is AdminNavTopic {
  return "items" in entry;
}

const PAGES = {
  dashboard: {
    label: "Dashboard",
    description: "Live sessions, content health, and server activity.",
    keywords: ["overview", "stats", "health", "scan all"],
    icon: LayoutDashboard,
    href: "/admin",
    exact: true,
  },
  activity: {
    label: "Activity",
    description: "Live streams and current playback sessions.",
    keywords: ["streams", "sessions", "now playing", "transcode"],
    icon: Radio,
    href: "/admin/activity",
  },
  logs: {
    label: "Logs",
    description: "Server log stream and operational output.",
    keywords: ["server logs", "debug", "tail", "events"],
    icon: ScrollText,
    href: "/admin/logs",
  },
  diagnostics: {
    label: "Diagnostics",
    description: "Uploaded client crash reports, device context, and debug bundles.",
    keywords: ["client diagnostics", "crash reports", "debug bundles", "support"],
    icon: FileWarning,
    href: "/admin/diagnostics",
  },
  libraries: {
    label: "Libraries",
    description: "Media libraries, paths, scanning, and catalog import.",
    keywords: ["library", "paths", "scan", "catalog", "seed"],
    icon: Library,
    href: "/admin/libraries",
  },
  autoscanSources: {
    label: "Sources",
    description: "What Autoscan watches for changes: folders, webhooks, and servers.",
    keywords: ["autoscan", "scan sources", "polling", "webhook source", "run now"],
    icon: Radar,
    href: "/admin/autoscan",
    // Activity is its own page below this path.
    exact: true,
  },
  autoscanActivity: {
    label: "Activity",
    description: "Changes Autoscan received and the scans they started.",
    keywords: ["autoscan", "scan queue", "scan events", "scan history"],
    icon: Radar,
    href: "/admin/autoscan/activity",
  },
  collections: {
    label: "Collections",
    description: "Curated and smart collection management.",
    keywords: ["collection groups", "templates", "smart collections"],
    icon: LayoutPanelTop,
    href: "/admin/collections",
  },
  sections: {
    label: "Sections",
    description: "Rows on Home and on library pages.",
    keywords: ["home rows", "rows", "rails", "featured sections"],
    icon: Rows2,
    href: "/admin/sections",
  },
  requests: {
    label: "Requests",
    description: "Review, approve, and decline media requests.",
    // Servers, routing, and the request limit itself live on the Requests
    // settings page, which the command palette lists with its own keywords.
    // A group's or an account's own limit lives with the group or account.
    keywords: ["requested media", "approvals", "request queue", "decline"],
    icon: Send,
    href: "/admin/requests",
  },
  scheduledTasks: {
    label: "Scheduled Tasks",
    description: "Background task schedules, runs, and job history.",
    keywords: ["tasks", "jobs", "scheduler", "sync"],
    icon: CalendarClock,
    href: "/admin/tasks",
  },
  subtitleFiles: {
    label: "Subtitle Files",
    description: "Downloaded subtitle records and subtitle admin tools.",
    keywords: ["captions", "subtitle downloads", "providers"],
    icon: Captions,
    href: "/admin/subtitles",
  },
  markers: {
    label: "Markers",
    description: "Intro, recap, and credits marker history.",
    keywords: ["intro markers", "credits", "recaps", "chapters"],
    icon: SkipForward,
    href: "/admin/marker-history",
  },
  recommendations: {
    label: "Recommendations",
    description: "Recommendation diagnostics, seed data, and ranking controls.",
    keywords: ["taste", "ranking", "recommendation seeds"],
    icon: Bot,
    href: "/admin/recommendations",
  },
  users: {
    label: "Users",
    description: "Accounts, roles, profile settings, and access.",
    keywords: ["accounts", "profiles", "roles", "permissions"],
    icon: Users,
    href: "/admin/users",
  },
  accessGroups: {
    label: "Access Groups",
    description: "Shared access defaults: libraries, downloads, streams, requests, permissions.",
    keywords: [
      "groups",
      "roles",
      "permissions",
      "library access",
      "downloads",
      "limits",
      "request limits",
    ],
    icon: UsersRound,
    href: "/admin/access-groups",
  },
  devices: {
    label: "Devices",
    description: "Registered devices, overrides, and per-device settings.",
    keywords: ["clients", "device overrides", "sessions"],
    icon: MonitorSmartphone,
    href: "/admin/devices",
  },
  playbackHistory: {
    label: "Playback History",
    description: "Historical playback events across users and profiles.",
    keywords: ["history", "watched", "progress", "plays"],
    icon: History,
    href: "/admin/history",
  },
  historyImport: {
    label: "History Import",
    description: "Admin history import mappings and bulk import runs.",
    keywords: ["emby", "imports", "mappings", "watch history"],
    icon: Download,
    href: "/admin/history-import",
  },
  plugins: {
    label: "Plugins",
    description: "Plugin catalog, repositories, installs, and plugin configuration.",
    keywords: ["extensions", "plugin catalog", "repositories"],
    icon: Blocks,
    href: "/admin/plugins",
  },
  policy: {
    label: "Policy",
    description: "OPA policy documents, vendor modules, simulations, and decision logs.",
    keywords: ["opa", "rego", "authorization", "decision log", "access policy"],
    icon: ShieldCheck,
    href: "/admin/policy",
  },
  nodes: {
    label: "Nodes",
    description: "Stream nodes, remote worker status, and where playback work runs.",
    keywords: [
      "stream nodes",
      "workers",
      "transcode nodes",
      "proxy nodes",
      "routing",
      "egress",
      "gpu offload",
      "central egress",
    ],
    settings: [
      { label: "Routing preset" },
      { label: "Direct play egress" },
      { label: "Remux execution" },
      { label: "Remux egress" },
      { label: "Video transcode execution" },
      { label: "Video transcode egress" },
    ],
    icon: Server,
    href: "/admin/nodes",
  },
  downloads: {
    label: "Downloads",
    description: "Prepared download storage on the server and nodes, and device copies.",
    keywords: [
      "offline downloads",
      "prepared files",
      "artifacts",
      "disk space",
      "storage",
      "revoke",
      "devices",
    ],
    icon: HardDriveDownload,
    href: "/admin/downloads",
  },
  apiKeys: {
    label: "API Keys",
    description: "Admin API keys and tier assignment.",
    keywords: ["tokens", "keys", "access", "rate limit tier"],
    icon: KeyRound,
    href: "/admin/api-keys",
  },
  maintenance: {
    label: "Maintenance",
    description: "Operational maintenance tools.",
    keywords: ["repair", "cleanup", "system maintenance"],
    icon: Wrench,
    href: "/admin/maintenance",
  },
} satisfies Record<string, AdminNavItem>;

/**
 * A settings page as a sidebar entry. Under a topic that also holds tools the
 * page is just "Settings"; the topic already says whose. Its own name stays a
 * keyword, so searching "Logging" still finds Logs › Settings.
 */
function settingsPage(id: string, label?: string): AdminNavItem {
  const page = ADMIN_SETTINGS_NAV.find((item) => item.id === id);
  if (!page) throw new Error(`Unknown admin settings page ${id}`);
  return {
    label: label ?? page.label,
    description: page.description,
    keywords: ["settings", "configuration", page.label, ...(page.keywords ?? [])],
    settings: page.settings,
    icon: page.icon,
    href: adminSettingsHref(id),
  };
}

/**
 * The admin sidebar. Tools and settings sit together under the topic they are
 * about, so an admin looks for "Requests", not "request settings"; a topic's own
 * settings page is its last entry. Every admin page appears exactly once.
 */
export const ADMIN_NAV_SECTIONS: AdminNavSection[] = [
  {
    label: "Monitor",
    entries: [
      PAGES.dashboard,
      PAGES.activity,
      {
        id: "logs",
        label: "Logs",
        icon: ScrollText,
        items: [
          { ...PAGES.logs, label: "Server Log" },
          { ...PAGES.diagnostics, label: "Client Diagnostics" },
          settingsPage("logging", "Settings"),
        ],
      },
    ],
  },
  {
    label: "Manage",
    entries: [
      {
        id: "libraries",
        label: "Libraries",
        icon: Library,
        items: [
          PAGES.libraries,
          PAGES.collections,
          { ...PAGES.sections, label: "Home Sections" },
          PAGES.subtitleFiles,
          settingsPage("library", "Settings"),
        ],
      },
      {
        id: "autoscan",
        label: "Autoscan",
        icon: Radar,
        items: [
          PAGES.autoscanActivity,
          PAGES.autoscanSources,
          settingsPage("autoscan", "Settings"),
        ],
      },
      {
        id: "requests",
        label: "Requests",
        icon: Send,
        items: [{ ...PAGES.requests, label: "Queue" }, settingsPage("requests", "Settings")],
      },
      {
        id: "users",
        label: "Users",
        icon: Users,
        items: [PAGES.users, PAGES.accessGroups, PAGES.devices, PAGES.historyImport],
      },
      {
        id: "playback",
        label: "Playback",
        icon: PlayCircle,
        items: [
          { ...PAGES.playbackHistory, label: "History" },
          { ...PAGES.markers, label: "Skip Markers" },
          PAGES.nodes,
          settingsPage("playback", "Settings"),
        ],
      },
      {
        id: "downloads",
        label: "Downloads",
        icon: HardDriveDownload,
        items: [
          { ...PAGES.downloads, label: "Prepared Files" },
          settingsPage("downloads", "Settings"),
        ],
      },
    ],
  },
  {
    label: "Configure",
    entries: [
      {
        id: "server",
        label: "Server",
        icon: Settings2,
        items: [
          settingsPage("general"),
          settingsPage("appearance"),
          settingsPage("storage"),
          settingsPage("search"),
          settingsPage("network-access"),
          PAGES.scheduledTasks,
          PAGES.maintenance,
        ],
      },
      {
        id: "access",
        label: "Sign-in & Security",
        icon: Shield,
        items: [settingsPage("sign-in"), settingsPage("security"), PAGES.apiKeys, PAGES.policy],
      },
      {
        id: "integrations",
        label: "Integrations",
        icon: Puzzle,
        items: [
          PAGES.plugins,
          settingsPage("providers"),
          settingsPage("email"),
          settingsPage("notifications"),
          settingsPage("ai"),
          PAGES.recommendations,
          settingsPage("compatibility"),
        ],
      },
    ],
  },
];

function visibleItem(item: AdminNavItem, visibility: AdminNavVisibility): boolean {
  return item.href !== "/admin/policy" || visibility.policyEditorAvailable === true;
}

export function buildAdminNavSections(visibility: AdminNavVisibility = {}): AdminNavSection[] {
  return ADMIN_NAV_SECTIONS.map((section) => ({
    ...section,
    entries: section.entries.flatMap((entry): AdminNavEntry[] => {
      if (!isAdminNavTopic(entry)) return visibleItem(entry, visibility) ? [entry] : [];
      const items = entry.items.filter((item) => visibleItem(item, visibility));
      return items.length > 0 ? [{ ...entry, items }] : [];
    }),
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

/**
 * The command palette's groups: the sidebar's loose pages under their section,
 * and each topic as its own group, so a result reads "Requests" over
 * "Settings" rather than a bare "Settings". Every setting each settings page
 * indexes is searchable through its entry.
 */
export function buildAdminCommandNavSections(
  installations: readonly PluginInstallation[] | undefined,
  visibility: AdminNavVisibility = {},
): AdminNavGroup[] {
  const groups: AdminNavGroup[] = [];
  for (const section of buildAdminNavSections(visibility)) {
    const loose = section.entries.filter((entry): entry is AdminNavItem => !isAdminNavTopic(entry));
    if (loose.length > 0) groups.push({ label: section.label, items: loose });
    for (const entry of section.entries) {
      if (isAdminNavTopic(entry)) groups.push({ label: entry.label, items: entry.items });
    }
  }

  return appendAdminPluginNavSection(groups, installations);
}
