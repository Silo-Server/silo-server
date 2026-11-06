export const REASON_FIELDS = [
  { key: "notify_favorites", label: "Favorites" },
  { key: "notify_watchlist", label: "Watchlist" },
  { key: "notify_continue_watching", label: "Continue Watching" },
  { key: "notify_next_up", label: "Next Up" },
] as const;

// Destinations (webhooks and plugin destinations) also carry the requests
// toggle; it is not an episode reason, so the profile preferences section
// keeps REASON_FIELDS.
export const DESTINATION_NOTIFY_FIELDS = [
  ...REASON_FIELDS,
  { key: "notify_requests", label: "Requests" },
] as const;

export type DestinationNotifyKey = (typeof DESTINATION_NOTIFY_FIELDS)[number]["key"];
