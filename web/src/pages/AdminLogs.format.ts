import type { OperationalLogEntry } from "@/api/types";
import { formatDateTime as formatPreferredDateTime } from "@/lib/datetime";
import type { Translate } from "@/i18n";

export function shortID(value: string) {
  if (value.length <= 12) return value;
  return `${value.slice(0, 8)}...${value.slice(-4)}`;
}

export function formatDateTime(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return formatPreferredDateTime(date);
}

export function formatConnectionState(t: Translate, state: "connecting" | "live" | "disconnected") {
  switch (state) {
    case "connecting":
      return t("web.admin.logs.connection.connecting");
    case "live":
      return t("web.admin.logs.connection.live");
    default:
      return t("web.admin.logs.connection.disconnected");
  }
}

export function formatClientIP(value: string) {
  if (!value) return "-";
  return value.replace(/\/\d+$/, "");
}

export function stringAttr(entry: OperationalLogEntry, key: string) {
  const value = entry.attrs?.[key];
  if (typeof value === "string" && value.length > 0) return value;
  if (typeof value === "number") return String(value);
  return "-";
}
