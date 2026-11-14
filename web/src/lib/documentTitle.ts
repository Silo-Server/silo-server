import { t, type MessageKey } from "@/i18n";

/** The product name: the tab title when admin branding names no server. */
const PRODUCT_NAME = "Silo";

/** Default app name, overridable by admin branding settings. */
export let APP_DOCUMENT_TITLE = PRODUCT_NAME;

/**
 * The label of the currently-mounted page (set by useDocumentTitle). Tracked
 * here so setAppDocumentTitle can re-apply the full title when branding loads
 * after the page has already set its title.
 */
let activeLabel: string | null = null;

/** Records the active page label so the title can be recomputed on rebrand. */
export function setActiveDocumentTitleLabel(label: string | null | undefined) {
  activeLabel = label ?? null;
}

/**
 * Called by the branding provider to update the document title prefix. Also
 * re-applies the current page's title so an already-rendered page reflects the
 * server name as soon as branding resolves.
 */
export function setAppDocumentTitle(name: string) {
  APP_DOCUMENT_TITLE = name || PRODUCT_NAME;
  if (typeof document !== "undefined") {
    document.title = formatDocumentTitle(activeLabel);
  }
}

const SETTINGS_TITLES: Record<string, MessageKey> = {
  account: "web.common.title.settings.account",
  sessions: "web.common.title.settings.sessions",
  interface: "web.common.title.settings.interface",
  accessibility: "web.common.title.settings.accessibility",
  playback: "web.common.title.settings.playback",
  profiles: "web.common.title.settings.profiles",
  libraries: "web.common.title.settings.libraries",
  requests: "web.common.title.settings.requests",
  "history-import": "web.common.title.settings.historyImport",
  "plex-webhooks": "web.common.title.settings.webhookSync",
  "webhook-sync": "web.common.title.settings.webhookSync",
  "subtitle-appearance": "web.common.title.settings.subtitleAppearance",
  "home-screen": "web.common.title.settings.homeScreen",
  "title-pages": "web.common.title.settings.titlePages",
  "connect-apps": "web.common.title.settings.connectApps",
  devices: "web.common.title.settings.devices",
  personalize: "web.common.title.settings.personalize",
  "watch-providers": "web.common.title.settings.watchProviders",
  notifications: "web.common.title.settings.notifications",
  "your-data": "web.common.title.settings.yourData",
};

/** An admin page's tab title and the shorter name the phone header shows. */
interface AdminTitle {
  tab: MessageKey;
  header: MessageKey;
}

/** Admin pages with a header name: each also has `web.admin.common.title.tab.<name>`. */
type AdminPageName = MessageKey extends infer K
  ? K extends `web.admin.common.title.header.${infer Name}`
    ? Name
    : never
  : never;

function adminPage(name: AdminPageName): AdminTitle {
  return {
    tab: `web.admin.common.title.tab.${name}`,
    header: `web.admin.common.title.header.${name}`,
  };
}

/** The dashboard, and any admin page without a title of its own. */
const ADMIN_ROOT: AdminTitle = {
  tab: "web.admin.common.title.tab.admin",
  header: "web.admin.shell.mobileHeader.dashboardTitle",
};

const ADMIN_TITLES: Record<string, AdminTitle> = {
  "access-groups": adminPage("accessGroups"),
  activity: adminPage("activity"),
  "api-keys": adminPage("apiKeys"),
  autoscan: adminPage("autoscan"),
  backups: adminPage("backups"),
  collections: adminPage("collections"),
  devices: adminPage("devices"),
  diagnostics: adminPage("diagnostics"),
  downloads: adminPage("downloads"),
  history: adminPage("history"),
  "history-import": adminPage("historyImport"),
  "marker-history": adminPage("markerHistory"),
  libraries: adminPage("libraries"),
  logs: adminPage("logs"),
  maintenance: adminPage("maintenance"),
  nodes: adminPage("nodes"),
  plugins: adminPage("plugins"),
  policy: adminPage("policy"),
  recommendations: adminPage("recommendations"),
  requests: adminPage("requests"),
  sections: adminPage("sections"),
  subtitles: adminPage("subtitles"),
  settings: adminPage("settings"),
  tasks: adminPage("tasks"),
  users: adminPage("users"),
};

export function formatDocumentTitle(label?: string | null): string {
  const normalized = label?.trim();
  if (!normalized) {
    return APP_DOCUMENT_TITLE;
  }
  return `${normalized} · ${APP_DOCUMENT_TITLE}`;
}

export function resolveSettingsDocumentTitle(pathname: string): string {
  const segments = pathname.split("/").filter(Boolean);
  const settingsSegment = segments[1];
  return t(SETTINGS_TITLES[settingsSegment ?? ""] ?? "web.common.title.settings.index");
}

function adminTitle(pathname: string): AdminTitle {
  const segments = pathname.split("/").filter(Boolean);
  const adminSegment = segments[1];
  const nestedSegment = segments[2];

  if (!adminSegment) {
    return ADMIN_ROOT;
  }

  if (adminSegment === "collections") {
    if (nestedSegment === "new") {
      const key = "web.admin.common.title.tab.collectionNew";
      return { tab: key, header: key };
    }
    if (segments[3] === "edit") {
      const key = "web.admin.common.title.tab.collectionEdit";
      return { tab: key, header: key };
    }
  }

  if (adminSegment === "tasks" && nestedSegment) {
    return adminPage("task");
  }

  if (adminSegment === "users" && nestedSegment) {
    return adminPage("user");
  }

  if (adminSegment === "autoscan" && nestedSegment === "activity") {
    return adminPage("autoscanActivity");
  }

  if (adminSegment === "plugins" && nestedSegment) {
    return adminPage("plugin");
  }

  return ADMIN_TITLES[adminSegment] ?? ADMIN_ROOT;
}

/** The admin page's browser tab title, before the server name. */
export function resolveAdminDocumentTitle(pathname: string): string {
  return t(adminTitle(pathname).tab);
}

/** The admin page's name in the phone header: the tab title without "Admin". */
export function resolveAdminHeaderTitle(pathname: string): string {
  return t(adminTitle(pathname).header);
}
