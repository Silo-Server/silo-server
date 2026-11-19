import type { MessageKey } from "@/i18n";

export const REASON_FIELDS = [
  { key: "notify_next_up", labelKey: "web.settings.notifications.reason.nextUp" },
] as const satisfies readonly { key: string; labelKey: MessageKey }[];

// Destinations (webhooks and plugin destinations) also carry the request
// toggle, which also covers watchlist.available; it is not an episode reason,
// so the profile preferences section keeps REASON_FIELDS.
export const DESTINATION_NOTIFY_FIELDS = [
  ...REASON_FIELDS,
  { key: "notify_requests", labelKey: "web.settings.notifications.reason.requests" },
] as const satisfies readonly { key: string; labelKey: MessageKey }[];

export type DestinationNotifyKey = (typeof DESTINATION_NOTIFY_FIELDS)[number]["key"];
