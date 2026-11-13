/*
 * Labels and descriptions here are message ids read through getters, the same
 * way as in lib/adminSettingsSearch (see the top of that file): `label`,
 * `description`, `keywords` and `settings[i].label` of every section, topic,
 * item and command-palette group return text in the current interface language
 * each time they are read. A component that shows them calls `useT()` or
 * `useAppLanguage()` so it re-renders on a language change. Spreading an item
 * (`{ ...item }`) freezes its text in the language of that moment, so the
 * builders below copy with getters (`withEntries`, `topicWith`, `groupOf`)
 * instead.
 * Keywords stay English; a translated interface also searches the English
 * label and description.
 */
import {
  Blocks,
  Bot,
  CalendarClock,
  Captions,
  Compass,
  DatabaseBackup,
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
import { t, type MessageKey } from "@/i18n";
import {
  ADMIN_SETTINGS_NAV,
  adminSettingsHref,
  englishSearchTerms,
  searchEntry,
} from "@/lib/adminSettingsSearch";
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
  /** The English heading, so an English search still finds the topic's pages. */
  keywords?: readonly string[];
  icon: LucideIcon;
  items: AdminNavItem[];
}

export type AdminNavEntry = AdminNavItem | AdminNavTopic;

export interface AdminNavSection {
  /** Stable key (React keys, element ids); the label is translated. */
  id: string;
  label: string;
  /** The English heading, so an English search still finds the section's pages. */
  keywords?: readonly string[];
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

/** An admin page before it becomes a sidebar item: its text as message ids. */
interface NavPageDefinition {
  labelKey: MessageKey;
  descriptionKey: MessageKey;
  /** English search synonyms; never shown. */
  keywords: readonly string[];
  /** Settings on the page, by their visible label, so search can find them. */
  settings?: readonly MessageKey[];
  icon: LucideIcon;
  href: string;
  exact?: boolean;
}

const PAGES = {
  dashboard: {
    labelKey: "web.admin.shell.nav.page.dashboard.label",
    descriptionKey: "web.admin.shell.nav.page.dashboard.description",
    keywords: ["overview", "stats", "health", "scan all"],
    icon: LayoutDashboard,
    href: "/admin",
    exact: true,
  },
  activity: {
    labelKey: "web.admin.shell.nav.page.activity.label",
    descriptionKey: "web.admin.shell.nav.page.activity.description",
    keywords: ["streams", "sessions", "now playing", "transcode"],
    icon: Radio,
    href: "/admin/activity",
  },
  logs: {
    labelKey: "web.admin.shell.nav.page.logs.label",
    descriptionKey: "web.admin.shell.nav.page.logs.description",
    keywords: ["server logs", "debug", "tail", "events"],
    icon: ScrollText,
    href: "/admin/logs",
  },
  diagnostics: {
    labelKey: "web.admin.shell.nav.page.diagnostics.label",
    descriptionKey: "web.admin.shell.nav.page.diagnostics.description",
    keywords: ["client diagnostics", "crash reports", "debug bundles", "support"],
    icon: FileWarning,
    href: "/admin/diagnostics",
  },
  libraries: {
    labelKey: "web.admin.shell.nav.page.libraries.label",
    descriptionKey: "web.admin.shell.nav.page.libraries.description",
    keywords: ["library", "paths", "scan", "catalog", "seed"],
    icon: Library,
    href: "/admin/libraries",
  },
  autoscanSources: {
    labelKey: "web.admin.shell.nav.page.autoscanSources.label",
    descriptionKey: "web.admin.shell.nav.page.autoscanSources.description",
    keywords: ["autoscan", "scan sources", "polling", "webhook source", "run now"],
    icon: Radar,
    href: "/admin/autoscan",
    // Activity is its own page below this path.
    exact: true,
  },
  autoscanActivity: {
    labelKey: "web.admin.shell.nav.page.autoscanActivity.label",
    descriptionKey: "web.admin.shell.nav.page.autoscanActivity.description",
    keywords: ["autoscan", "scan queue", "scan events", "scan history"],
    icon: Radar,
    href: "/admin/autoscan/activity",
  },
  collections: {
    labelKey: "web.admin.shell.nav.page.collections.label",
    descriptionKey: "web.admin.shell.nav.page.collections.description",
    keywords: ["collection groups", "templates", "smart collections"],
    icon: LayoutPanelTop,
    href: "/admin/collections",
  },
  sections: {
    labelKey: "web.admin.shell.nav.page.sections.label",
    descriptionKey: "web.admin.shell.nav.page.sections.description",
    keywords: ["home rows", "rows", "rails", "featured sections"],
    icon: Rows2,
    href: "/admin/sections",
  },
  requests: {
    labelKey: "web.admin.shell.nav.page.requests.label",
    descriptionKey: "web.admin.shell.nav.page.requests.description",
    // Servers, routing, and the request limit itself live on the Requests
    // settings page, which the command palette lists with its own keywords.
    // A group's or an account's own limit lives with the group or account.
    keywords: ["requested media", "approvals", "request queue", "decline"],
    icon: Send,
    href: "/admin/requests",
    // The Discover page editor is its own page below this path.
    exact: true,
  },
  requestDiscover: {
    labelKey: "web.admin.shell.nav.page.requestDiscover.label",
    descriptionKey: "web.admin.shell.nav.page.requestDiscover.description",
    keywords: [
      "discover rows",
      "request rows",
      "mdblist",
      "tmdb list",
      "trending",
      "discover filters",
    ],
    icon: Compass,
    href: "/admin/requests/discover",
  },
  scheduledTasks: {
    labelKey: "web.admin.shell.nav.page.scheduledTasks.label",
    descriptionKey: "web.admin.shell.nav.page.scheduledTasks.description",
    keywords: ["tasks", "jobs", "scheduler", "sync"],
    icon: CalendarClock,
    href: "/admin/tasks",
  },
  subtitleFiles: {
    labelKey: "web.admin.shell.nav.page.subtitleFiles.label",
    descriptionKey: "web.admin.shell.nav.page.subtitleFiles.description",
    keywords: ["captions", "subtitle downloads", "providers"],
    icon: Captions,
    href: "/admin/subtitles",
  },
  markers: {
    labelKey: "web.admin.shell.nav.page.markers.label",
    descriptionKey: "web.admin.shell.nav.page.markers.description",
    keywords: ["intro markers", "credits", "recaps", "chapters"],
    icon: SkipForward,
    href: "/admin/marker-history",
  },
  recommendations: {
    labelKey: "web.admin.shell.nav.page.recommendations.label",
    descriptionKey: "web.admin.shell.nav.page.recommendations.description",
    keywords: ["taste", "ranking", "recommendation seeds"],
    icon: Bot,
    href: "/admin/recommendations",
  },
  users: {
    labelKey: "web.admin.shell.nav.page.users.label",
    descriptionKey: "web.admin.shell.nav.page.users.description",
    keywords: ["accounts", "profiles", "roles", "permissions"],
    icon: Users,
    href: "/admin/users",
  },
  accessGroups: {
    labelKey: "web.admin.shell.nav.page.accessGroups.label",
    descriptionKey: "web.admin.shell.nav.page.accessGroups.description",
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
    labelKey: "web.admin.shell.nav.page.devices.label",
    descriptionKey: "web.admin.shell.nav.page.devices.description",
    keywords: ["clients", "device overrides", "sessions"],
    icon: MonitorSmartphone,
    href: "/admin/devices",
  },
  playbackHistory: {
    labelKey: "web.admin.shell.nav.page.playbackHistory.label",
    descriptionKey: "web.admin.shell.nav.page.playbackHistory.description",
    keywords: ["history", "watched", "progress", "plays"],
    icon: History,
    href: "/admin/history",
  },
  historyImport: {
    labelKey: "web.admin.shell.nav.page.historyImport.label",
    descriptionKey: "web.admin.shell.nav.page.historyImport.description",
    keywords: ["emby", "imports", "mappings", "watch history"],
    icon: Download,
    href: "/admin/history-import",
  },
  plugins: {
    labelKey: "web.admin.shell.nav.page.plugins.label",
    descriptionKey: "web.admin.shell.nav.page.plugins.description",
    keywords: ["extensions", "plugin catalog", "repositories"],
    icon: Blocks,
    href: "/admin/plugins",
  },
  policy: {
    labelKey: "web.admin.shell.nav.page.policy.label",
    descriptionKey: "web.admin.shell.nav.page.policy.description",
    keywords: ["opa", "rego", "authorization", "decision log", "access policy"],
    icon: ShieldCheck,
    href: "/admin/policy",
  },
  nodes: {
    labelKey: "web.admin.shell.nav.page.nodes.label",
    descriptionKey: "web.admin.shell.nav.page.nodes.description",
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
      "web.admin.shell.nav.page.nodes.setting.routingPreset",
      "web.admin.shell.nav.page.nodes.setting.directPlayEgress",
      "web.admin.shell.nav.page.nodes.setting.remuxExecution",
      "web.admin.shell.nav.page.nodes.setting.remuxEgress",
      "web.admin.shell.nav.page.nodes.setting.videoTranscodeExecution",
      "web.admin.shell.nav.page.nodes.setting.videoTranscodeEgress",
    ],
    icon: Server,
    href: "/admin/nodes",
  },
  downloads: {
    labelKey: "web.admin.shell.nav.page.downloads.label",
    descriptionKey: "web.admin.shell.nav.page.downloads.description",
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
  backups: {
    labelKey: "web.admin.shell.nav.page.backups.label",
    descriptionKey: "web.admin.shell.nav.page.backups.description",
    keywords: [
      "backup",
      "back up now",
      "restore",
      "recovery",
      "database dump",
      "pg_dump",
      "disaster recovery",
      "copies to keep",
      "retention",
    ],
    icon: DatabaseBackup,
    href: "/admin/backups",
  },
  apiKeys: {
    labelKey: "web.admin.shell.nav.page.apiKeys.label",
    descriptionKey: "web.admin.shell.nav.page.apiKeys.description",
    keywords: ["tokens", "keys", "access", "scopes", "rate limit tier"],
    icon: KeyRound,
    href: "/admin/api-keys",
  },
  maintenance: {
    labelKey: "web.admin.shell.nav.page.maintenance.label",
    descriptionKey: "web.admin.shell.nav.page.maintenance.description",
    keywords: ["repair", "cleanup", "system maintenance"],
    icon: Wrench,
    href: "/admin/maintenance",
  },
} satisfies Record<string, NavPageDefinition>;

/** A page as a sidebar item; `labelKey` names it differently under a topic. */
function navPage(page: NavPageDefinition, labelKey: MessageKey = page.labelKey): AdminNavItem {
  return {
    get label() {
      return t(labelKey);
    },
    get description() {
      return t(page.descriptionKey);
    },
    get keywords() {
      return [...page.keywords, ...englishSearchTerms(labelKey, page.descriptionKey)];
    },
    settings: page.settings?.map(searchEntry),
    icon: page.icon,
    href: page.href,
    exact: page.exact,
  };
}

/**
 * A settings page as a sidebar entry. Under a topic that also holds tools the
 * page is just "Settings"; the topic already says whose. Its own name stays a
 * keyword, so searching "Logging" still finds Logs › Settings.
 */
function settingsPage(id: string, labelKey?: MessageKey): AdminNavItem {
  const page = ADMIN_SETTINGS_NAV.find((item) => item.id === id);
  if (!page) throw new Error(`Unknown admin settings page ${id}`);
  return {
    get label() {
      return labelKey ? t(labelKey) : page.label;
    },
    get description() {
      return page.description;
    },
    get keywords() {
      return [
        "settings",
        "configuration",
        page.label,
        ...(labelKey ? englishSearchTerms(labelKey) : []),
        ...(page.keywords ?? []),
      ];
    },
    settings: page.settings,
    icon: page.icon,
    href: adminSettingsHref(id),
  };
}

const SETTINGS: MessageKey = "web.admin.shell.nav.item.settings";

function navSection(id: string, labelKey: MessageKey, entries: AdminNavEntry[]): AdminNavSection {
  return {
    id,
    get label() {
      return t(labelKey);
    },
    get keywords() {
      return englishSearchTerms(labelKey);
    },
    entries,
  };
}

function navTopic(
  id: string,
  labelKey: MessageKey,
  icon: LucideIcon,
  items: AdminNavItem[],
): AdminNavTopic {
  return {
    id,
    get label() {
      return t(labelKey);
    },
    get keywords() {
      return englishSearchTerms(labelKey);
    },
    icon,
    items,
  };
}

/** `section` with other entries, keeping its label live. */
function withEntries(section: AdminNavSection, entries: AdminNavEntry[]): AdminNavSection {
  return {
    id: section.id,
    get label() {
      return section.label;
    },
    get keywords() {
      return section.keywords;
    },
    entries,
  };
}

/** `topic` with other items, keeping its label live. */
function topicWith(topic: AdminNavTopic, items: AdminNavItem[]): AdminNavTopic {
  return {
    id: topic.id,
    get label() {
      return topic.label;
    },
    get keywords() {
      return topic.keywords;
    },
    icon: topic.icon,
    items,
  };
}

/** A command-palette group named like `source` (a section, topic or group), label kept live. */
function groupOf(
  source: { readonly label: string; readonly keywords?: readonly string[] },
  items: readonly AdminNavItem[],
): AdminNavGroup {
  return {
    get label() {
      return source.label;
    },
    get keywords() {
      return source.keywords;
    },
    items,
  };
}

/**
 * The admin sidebar. Tools and settings sit together under the topic they are
 * about, so an admin looks for "Requests", not "request settings"; a topic's own
 * settings page is its last entry. Every admin page appears exactly once.
 */
export const ADMIN_NAV_SECTIONS: AdminNavSection[] = [
  navSection("monitor", "web.admin.shell.nav.section.monitor", [
    navPage(PAGES.dashboard),
    navPage(PAGES.activity),
    navTopic("logs", "web.admin.shell.nav.topic.logs", ScrollText, [
      navPage(PAGES.logs, "web.admin.shell.nav.item.serverLog"),
      navPage(PAGES.diagnostics, "web.admin.shell.nav.item.clientDiagnostics"),
      settingsPage("logging", SETTINGS),
    ]),
  ]),
  navSection("manage", "web.admin.shell.nav.section.manage", [
    navTopic("libraries", "web.admin.shell.nav.topic.libraries", Library, [
      navPage(PAGES.libraries),
      navPage(PAGES.collections),
      navPage(PAGES.sections, "web.admin.shell.nav.item.homeSections"),
      navPage(PAGES.subtitleFiles),
      settingsPage("library", SETTINGS),
    ]),
    navTopic("autoscan", "web.admin.shell.nav.topic.autoscan", Radar, [
      navPage(PAGES.autoscanActivity),
      navPage(PAGES.autoscanSources),
      settingsPage("autoscan", SETTINGS),
    ]),
    navTopic("requests", "web.admin.shell.nav.topic.requests", Send, [
      navPage(PAGES.requests, "web.admin.shell.nav.item.queue"),
      navPage(PAGES.requestDiscover),
      settingsPage("requests", SETTINGS),
    ]),
    navTopic("users", "web.admin.shell.nav.topic.users", Users, [
      navPage(PAGES.users),
      navPage(PAGES.accessGroups),
      navPage(PAGES.devices),
      navPage(PAGES.historyImport),
    ]),
    navTopic("playback", "web.admin.shell.nav.topic.playback", PlayCircle, [
      navPage(PAGES.playbackHistory, "web.admin.shell.nav.item.history"),
      navPage(PAGES.markers, "web.admin.shell.nav.item.skipMarkers"),
      navPage(PAGES.nodes),
      settingsPage("playback", SETTINGS),
    ]),
    navTopic("downloads", "web.admin.shell.nav.topic.downloads", HardDriveDownload, [
      navPage(PAGES.downloads, "web.admin.shell.nav.item.preparedFiles"),
      settingsPage("downloads", SETTINGS),
    ]),
  ]),
  navSection("configure", "web.admin.shell.nav.section.configure", [
    navTopic("server", "web.admin.shell.nav.topic.server", Settings2, [
      settingsPage("general"),
      settingsPage("appearance"),
      settingsPage("storage"),
      settingsPage("search"),
      settingsPage("network-access"),
      navPage(PAGES.scheduledTasks),
      navPage(PAGES.maintenance),
      navPage(PAGES.backups),
    ]),
    navTopic("access", "web.admin.shell.nav.topic.access", Shield, [
      settingsPage("sign-in"),
      settingsPage("security"),
      navPage(PAGES.apiKeys),
      navPage(PAGES.policy),
    ]),
    navTopic("integrations", "web.admin.shell.nav.topic.integrations", Puzzle, [
      navPage(PAGES.plugins),
      settingsPage("providers"),
      settingsPage("email"),
      settingsPage("notifications"),
      settingsPage("ai"),
      navPage(PAGES.recommendations),
      settingsPage("compatibility"),
    ]),
  ]),
];

function visibleItem(item: AdminNavItem, visibility: AdminNavVisibility): boolean {
  return item.href !== "/admin/policy" || visibility.policyEditorAvailable === true;
}

export function buildAdminNavSections(visibility: AdminNavVisibility = {}): AdminNavSection[] {
  return ADMIN_NAV_SECTIONS.map((section) =>
    withEntries(
      section,
      section.entries.flatMap((entry): AdminNavEntry[] => {
        if (!isAdminNavTopic(entry)) return visibleItem(entry, visibility) ? [entry] : [];
        const items = entry.items.filter((item) => visibleItem(item, visibility));
        return items.length > 0 ? [topicWith(entry, items)] : [];
      }),
    ),
  );
}

export function buildAdminPluginNavItems(
  installations: readonly PluginInstallation[] | undefined,
): AdminNavItem[] {
  const items: AdminNavItem[] = [];

  for (const installation of installations ?? []) {
    if (!installation.enabled) continue;

    for (const route of installation.routes ?? []) {
      if (!route.navigable || route.navigation_kind !== "admin") continue;

      // The plugin's own name for its page, or its id: plugin data, shown as is.
      const label = route.navigation_label || installation.plugin_id;
      items.push({
        label,
        get description() {
          return t("web.admin.shell.nav.pluginApp.description", {
            plugin: installation.plugin_id,
          });
        },
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
  const copies = sections.map((section) => groupOf(section, [...section.items]));

  if (!pluginItems.length) return copies;

  return [
    ...copies,
    {
      get label() {
        return t("web.admin.shell.nav.group.pluginApps");
      },
      items: pluginItems,
    },
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
    if (loose.length > 0) groups.push(groupOf(section, loose));
    for (const entry of section.entries) {
      if (isAdminNavTopic(entry)) groups.push(groupOf(entry, entry.items));
    }
  }

  return appendAdminPluginNavSection(groups, installations);
}
