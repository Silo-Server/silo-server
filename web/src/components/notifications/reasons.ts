/** The reasons a profile hears about a series, keyed like a notification's
 * reason_flags, with the label each reason pill shows. */
export const NOTIFICATION_REASONS = [
  { key: "favorite", label: "Favorite" },
  { key: "watchlist", label: "Watchlist" },
  { key: "continue_watching", label: "Continue Watching" },
  { key: "next_up", label: "Next Up" },
] as const;
