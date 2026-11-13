import type { AdminUser } from "@/api/types";
import { englishText, type MessageKey } from "@/i18n";
import { accountRoleLabel } from "@/lib/accountOwner";
import { formatLocale } from "@/lib/appLocale";
import { formatDateTime as formatDateTimePreferred } from "@/lib/datetime";

export type UserSortField =
  | "username"
  | "email"
  | "role"
  | "enabled"
  | "created_at"
  | "last_active_at";
export type SortDirection = "asc" | "desc";

/** The sortable fields in column order, labeled with their table headers. */
export const USER_SORT_FIELDS: { value: UserSortField; label: MessageKey }[] = [
  { value: "username", label: "web.admin.users.list.column.username" },
  { value: "email", label: "web.admin.users.list.column.email" },
  { value: "role", label: "web.admin.users.list.column.role" },
  { value: "enabled", label: "web.admin.users.list.column.status" },
  { value: "created_at", label: "web.admin.users.list.column.created" },
  { value: "last_active_at", label: "web.admin.users.list.column.lastActive" },
];

/** The direction a field starts in when it becomes the sort: newest first for dates. */
export function defaultSortDirection(field: UserSortField): SortDirection {
  return field === "created_at" || field === "last_active_at" ? "desc" : "asc";
}

export function sortAdminUsers(users: AdminUser[], field: UserSortField, dir: SortDirection) {
  const direction = dir === "asc" ? 1 : -1;

  return [...users].sort((a, b) => {
    let result = 0;
    switch (field) {
      case "username":
        result = compareText(a.username, b.username);
        break;
      case "email":
        result = compareText(a.email, b.email);
        break;
      case "role":
        result = compareText(accountRoleLabel(a, englishText), accountRoleLabel(b, englishText));
        break;
      case "enabled":
        result = compareText(a.enabled ? "active" : "disabled", b.enabled ? "active" : "disabled");
        break;
      case "created_at":
        result = compareTime(a.created_at, b.created_at, dir);
        break;
      case "last_active_at":
        result = compareTime(a.last_active_at, b.last_active_at, dir);
        break;
    }

    if (result !== 0) {
      return field === "created_at" || field === "last_active_at" ? result : result * direction;
    }

    return compareText(a.username, b.username) || a.id - b.id;
  });
}

function compareText(a?: string | null, b?: string | null) {
  return (a ?? "").localeCompare(b ?? "", undefined, { numeric: true, sensitivity: "base" });
}

function compareTime(a?: string | null, b?: string | null, dir: SortDirection = "asc") {
  const aTime = parseTime(a);
  const bTime = parseTime(b);
  if (aTime === null && bTime === null) return 0;
  if (aTime === null) return 1;
  if (bTime === null) return -1;
  return (aTime - bTime) * (dir === "asc" ? 1 : -1);
}

function parseTime(value?: string | null) {
  const timestamp = Date.parse(value ?? "");
  return Number.isNaN(timestamp) ? null : timestamp;
}

export function formatDateTime(value?: string | null, fallback = "-") {
  const timestamp = parseTime(value);
  if (timestamp === null) return fallback;
  return formatDateTimePreferred(timestamp, { dateStyle: "medium", seconds: false });
}

export function formatFullDateTime(value?: string | null) {
  const timestamp = parseTime(value);
  if (timestamp === null) return "";
  return formatDateTimePreferred(timestamp);
}

export function formatRelativeTime(value?: string | null, fallback = "-") {
  const timestamp = parseTime(value);
  if (timestamp === null) return fallback;

  const seconds = Math.round((timestamp - Date.now()) / 1000);
  const ranges: Array<[Intl.RelativeTimeFormatUnit, number]> = [
    ["year", 60 * 60 * 24 * 365],
    ["month", 60 * 60 * 24 * 30],
    ["week", 60 * 60 * 24 * 7],
    ["day", 60 * 60 * 24],
    ["hour", 60 * 60],
    ["minute", 60],
    ["second", 1],
  ];
  const formatter = new Intl.RelativeTimeFormat(formatLocale(), { numeric: "always" });

  for (const [unit, secondsPerUnit] of ranges) {
    if (Math.abs(seconds) >= secondsPerUnit || unit === "second") {
      return formatter.format(Math.round(seconds / secondsPerUnit), unit);
    }
  }

  return fallback;
}
