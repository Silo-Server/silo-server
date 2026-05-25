/** Default app name, overridable by admin branding settings. */
export let APP_DOCUMENT_TITLE = "Silo";

/** Called by the branding provider to update the document title prefix. */
export function setAppDocumentTitle(name: string) {
  APP_DOCUMENT_TITLE = name || "Silo";
}

const SETTINGS_TITLES: Record<string, string> = {
  appearance: "Appearance Settings",
  accessibility: "Accessibility Settings",
  playback: "Playback Settings",
  profiles: "Profile Settings",
  libraries: "Library Settings",
  "history-import": "History Import Settings",
  "plex-webhooks": "Webhook Sync Settings",
  "webhook-sync": "Webhook Sync Settings",
  "subtitle-appearance": "Subtitle Settings",
  "home-screen": "Home Screen Settings",
  "card-overlays": "Card Overlay Settings",
};

const ADMIN_TITLES: Record<string, string> = {
  activity: "Admin Activity",
  "api-keys": "Admin API Keys",
  collections: "Admin Collections",
  libraries: "Admin Libraries",
  logs: "Admin Logs",
  maintenance: "Admin Maintenance",
  nodes: "Admin Nodes",
  recommendations: "Admin Recommendations",
  requests: "Admin Requests",
  sections: "Admin Sections",
  settings: "Admin Settings",
  tasks: "Admin Tasks",
  users: "Admin Users",
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
  return SETTINGS_TITLES[settingsSegment ?? ""] ?? "Settings";
}

export function resolveAdminDocumentTitle(pathname: string): string {
  const segments = pathname.split("/").filter(Boolean);
  const adminSegment = segments[1];
  const nestedSegment = segments[2];

  if (!adminSegment) {
    return "Admin";
  }

  if (adminSegment === "collections") {
    if (nestedSegment === "new") {
      return "New Admin Collection";
    }
    if (segments[3] === "edit") {
      return "Edit Admin Collection";
    }
  }

  if (adminSegment === "tasks" && nestedSegment) {
    return "Admin Task";
  }

  if (adminSegment === "users" && nestedSegment) {
    return "Admin User";
  }

  return ADMIN_TITLES[adminSegment] ?? "Admin";
}
