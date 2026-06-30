import type { AdminACLCondition, AdminACLRuleWriteRequest } from "@/api/types";

export const ACCESS_GROUP_ACTION_CATEGORIES = [
  {
    id: "administration",
    label: "Administration",
    description: "Server-wide administration tools, security controls, logs, and settings.",
  },
  {
    id: "users",
    label: "Users",
    description: "People, profiles, requests, and user-owned lists.",
  },
  {
    id: "content",
    label: "Content",
    description: "Libraries, metadata, markers, and recommendation management.",
  },
  {
    id: "automation",
    label: "Automation",
    description: "Background tasks, plugins, and remote node administration.",
  },
  {
    id: "playback",
    label: "Playback",
    description: "Reading, listening, streaming, transcoding, and downloads.",
  },
] as const;

type AccessGroupActionCategory = (typeof ACCESS_GROUP_ACTION_CATEGORIES)[number]["id"];

export const ACCESS_GROUP_ACTIONS = [
  {
    action: "server.view",
    label: "Server View",
    category: "administration",
    description: "Allows access to admin overview pages and server status screens.",
  },
  {
    action: "server.configure",
    label: "Server Configuration",
    category: "administration",
    description: "Allows changing server settings that affect global behavior.",
  },
  {
    action: "security.manage",
    label: "Security Management",
    category: "administration",
    description: "Allows changing access groups, permissions, and security-sensitive settings.",
  },
  {
    action: "logs.view",
    label: "View Logs",
    category: "administration",
    description: "Allows viewing operational logs and diagnostic history.",
  },
  {
    action: "users.view",
    label: "View Users",
    category: "users",
    description: "Allows viewing user accounts, profiles, devices, and access details.",
  },
  {
    action: "users.manage",
    label: "Manage Users",
    category: "users",
    description: "Allows creating, editing, disabling, and deleting user accounts.",
  },
  {
    action: "users.impersonate",
    label: "Impersonate Users",
    category: "users",
    description: "Allows starting an admin impersonation session for another user.",
  },
  {
    action: "profiles.manage",
    label: "Manage Profiles",
    category: "users",
    description: "Allows creating, editing, and deleting profiles on user accounts.",
  },
  {
    action: "personal_lists.manage",
    label: "Manage Personal Lists",
    category: "users",
    description: "Allows changing favorites, watchlists, and other personal lists.",
  },
  {
    action: "requests.create",
    label: "Create Requests",
    category: "users",
    description: "Allows submitting requests for new media or content changes.",
  },
  {
    action: "requests.approve",
    label: "Approve Requests",
    category: "users",
    description: "Allows approving, declining, retrying, and managing user requests.",
  },
  {
    action: "libraries.view",
    label: "View Libraries",
    category: "content",
    description: "Allows viewing configured libraries, sections, and library details.",
  },
  {
    action: "libraries.manage",
    label: "Manage Libraries",
    category: "content",
    description: "Allows creating, editing, scanning, and deleting libraries.",
  },
  {
    action: "metadata.curate",
    label: "Metadata Curation",
    category: "content",
    description: "Allows editing metadata, matching items, and refreshing metadata.",
  },
  {
    action: "markers.edit",
    label: "Marker Editing",
    category: "content",
    description: "Allows editing intro, credits, and other playback markers.",
  },
  {
    action: "recommendations.view",
    label: "View Recommendations",
    category: "content",
    description: "Allows viewing recommendation configuration and status.",
  },
  {
    action: "recommendations.manage",
    label: "Manage Recommendations",
    category: "content",
    description: "Allows changing recommendation settings, rebuilds, and curation inputs.",
  },
  {
    action: "tasks.view",
    label: "View Tasks",
    category: "automation",
    description: "Allows viewing scheduled tasks, histories, and run status.",
  },
  {
    action: "tasks.run",
    label: "Run Tasks",
    category: "automation",
    description: "Allows manually starting maintenance, scan, and rebuild tasks.",
  },
  {
    action: "plugins.view",
    label: "View Plugins",
    category: "automation",
    description: "Allows viewing installed plugins, plugin status, and plugin configuration.",
  },
  {
    action: "plugins.manage",
    label: "Manage Plugins",
    category: "automation",
    description: "Allows installing, updating, configuring, and removing plugins.",
  },
  {
    action: "nodes.view",
    label: "View Nodes",
    category: "automation",
    description: "Allows viewing remote transcode nodes and node health.",
  },
  {
    action: "nodes.manage",
    label: "Manage Nodes",
    category: "automation",
    description: "Allows adding, editing, removing, and testing remote nodes.",
  },
  {
    action: "playback.play",
    label: "Read / Listen / Play",
    category: "playback",
    description: "Allows reading ebooks, listening to audiobooks, and playing video.",
  },
  {
    action: "playback.transcode",
    label: "Transcoding",
    category: "playback",
    description: "Allows playback that requires server-side transcoding.",
  },
  {
    action: "downloads.direct",
    label: "Direct Downloads",
    category: "playback",
    description: "Allows downloading original files without conversion.",
  },
  {
    action: "downloads.transcode",
    label: "Transcoded Downloads",
    category: "playback",
    description: "Allows creating converted downloads for compatibility or smaller files.",
  },
] as const satisfies readonly {
  action: string;
  label: string;
  category: AccessGroupActionCategory;
  description: string;
}[];

export type AccessGroupAction = (typeof ACCESS_GROUP_ACTIONS)[number]["action"];

export const ACCESS_GROUP_PRESETS = [
  {
    id: "junior_admin",
    label: "Junior Admin",
    description: "Can view the server, manage users, and handle requests without library control.",
    actions: ["server.view", "users.view", "users.manage", "requests.approve"],
  },
  {
    id: "request_manager",
    label: "Request Manager",
    description: "Can create and approve requests without broader admin access.",
    actions: ["requests.create", "requests.approve"],
  },
  {
    id: "library_steward",
    label: "Library Steward",
    description: "Can manage libraries and curate metadata without managing users or security.",
    actions: ["server.view", "libraries.manage", "metadata.curate", "markers.edit", "tasks.view"],
  },
] as const satisfies readonly {
  id: string;
  label: string;
  description: string;
  actions: readonly AccessGroupAction[];
}[];

const MEDIA_ACTIONS = new Set<AccessGroupAction>([
  "playback.play",
  "playback.transcode",
  "downloads.direct",
  "downloads.transcode",
  "personal_lists.manage",
  "metadata.curate",
  "markers.edit",
]);

const GROUP_ACTIONS = new Set<AccessGroupAction>(["requests.create", "requests.approve"]);

const USER_ACTIONS = new Set<AccessGroupAction>([
  "users.view",
  "users.manage",
  "users.impersonate",
]);

const LIBRARY_ACTIONS = new Set<AccessGroupAction>(["libraries.view", "libraries.manage"]);

const TASK_ACTIONS = new Set<AccessGroupAction>(["tasks.view", "tasks.run"]);

const PLUGIN_ACTIONS = new Set<AccessGroupAction>(["plugins.view", "plugins.manage"]);

const NODE_ACTIONS = new Set<AccessGroupAction>(["nodes.view", "nodes.manage"]);

export function accessGroupSlugFromName(name: string): string {
  const slug = name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_+|_+$/g, "")
    .replace(/_{2,}/g, "_");
  if (!slug) return "";
  return /^[a-z]/.test(slug) ? slug : `group_${slug}`;
}

export function ruleResourceTypeForAction(action: AccessGroupAction): string {
  if (MEDIA_ACTIONS.has(action)) return "media_item";
  if (GROUP_ACTIONS.has(action)) return "request";
  if (USER_ACTIONS.has(action)) return "user";
  if (LIBRARY_ACTIONS.has(action)) return "library";
  if (TASK_ACTIONS.has(action)) return "task";
  if (action === "logs.view") return "log";
  if (PLUGIN_ACTIONS.has(action)) return "plugin";
  if (NODE_ACTIONS.has(action)) return "remote_node";
  if (action === "profiles.manage") return "profile";
  if (action === "security.manage") return "security_settings";
  return "server";
}

export function buildAccessGroupRuleDrafts(
  actions: readonly AccessGroupAction[],
  conditions: AdminACLCondition,
): AdminACLRuleWriteRequest[] {
  return actions.map((action) => ({
    action,
    resource_type: ruleResourceTypeForAction(action),
    resource_id: "*",
    effect: "allow",
    priority: 50,
    name: accessGroupActionLabel(action),
    description: "",
    conditions: { ...conditions },
  }));
}

export function accessGroupActionLabel(action: string): string {
  return ACCESS_GROUP_ACTIONS.find((item) => item.action === action)?.label ?? action;
}

export function accessGroupActionDescription(action: string): string {
  return ACCESS_GROUP_ACTIONS.find((item) => item.action === action)?.description ?? "";
}
