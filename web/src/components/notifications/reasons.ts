import type { MessageKey } from "@/i18n";

/** The reasons a profile hears about a series, keyed like a notification's
 * reason_flags, with the message each reason pill shows (translate at render). */
export const NOTIFICATION_REASONS = [
  { key: "favorite", labelKey: "web.notifications.reason.favorite" },
  { key: "watchlist", labelKey: "web.notifications.reason.watchlist" },
  { key: "continue_watching", labelKey: "web.notifications.reason.continueWatching" },
  { key: "next_up", labelKey: "web.notifications.reason.nextUp" },
] as const satisfies readonly { key: string; labelKey: MessageKey }[];
