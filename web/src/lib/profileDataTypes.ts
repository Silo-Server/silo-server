import type { components } from "@/api/v2/schema";

/**
 * The kinds of a profile's own data, shared by a profile's export and import
 * and by restoring accounts from a backup.
 */
export type ProfileDataTypeId = components["schemas"]["ProfileImportData"]["type"];

/** Data types in the order exports, imports, and restores apply them. */
export const DATA_TYPE_IDS: readonly ProfileDataTypeId[] = [
  "watch_history",
  "ratings",
  "favorites",
  "collections",
  "dismissals",
  "preferences",
  "notifications",
];

export const DATA_TYPE_LABELS: Record<ProfileDataTypeId, string> = {
  watch_history: "Watch history and progress",
  ratings: "Ratings",
  favorites: "Favorites and watchlist",
  collections: "Personal collections",
  dismissals: 'Dismissed and "not interested"',
  preferences: "Playback and display preferences",
  notifications: "Notification preferences",
};

/** The same, inside a sentence. */
export const DATA_TYPE_PHRASES: Record<ProfileDataTypeId, string> = {
  watch_history: "watch history and progress",
  ratings: "ratings",
  favorites: "favorites and watchlist",
  collections: "personal collections",
  dismissals: 'dismissed and "not interested" titles',
  preferences: "playback and display preferences",
  notifications: "notification preferences",
};

/** One data type's merge counts: "3 added, 1 updated, 10 already here, 2 left out". */
export function dataCountsPhrase(data: {
  added: number;
  updated: number;
  unchanged: number;
  left_out: number;
}): string {
  const parts = [
    data.added > 0 ? `${data.added} added` : null,
    data.updated > 0 ? `${data.updated} updated` : null,
    data.unchanged > 0 ? `${data.unchanged} already here` : null,
    data.left_out > 0 ? `${data.left_out} left out` : null,
  ].filter((part): part is string => part !== null);
  return parts.length > 0 ? parts.join(", ") : "nothing to merge";
}

/** The most titles listed by name before "and N more". */
const TITLES_SHOWN = 10;

/** Titles an import or restore left out: "A, B, C, and 12 more". */
export function titleList(titles: readonly string[]): string {
  const shown = titles.slice(0, TITLES_SHOWN).join(", ");
  const rest = titles.length - TITLES_SHOWN;
  return rest > 0 ? `${shown}, and ${rest} more` : shown;
}

/** Short nouns for counting pending entries: one and many. */
const PENDING_NOUNS: Record<ProfileDataTypeId, [string, string]> = {
  watch_history: ["play", "plays"],
  ratings: ["rating", "ratings"],
  favorites: ["favorite", "favorites"],
  collections: ["collection", "collections"],
  dismissals: ["dismissal", "dismissals"],
  preferences: ["preference", "preferences"],
  notifications: ["notification setting", "notification settings"],
};

/**
 * What a restore would add for one data type. A matched profile reports
 * `pending`; a new profile has none, so all of its `count` is added.
 */
export function pendingCount(data: { count: number; pending?: number }): number {
  return data.pending ?? data.count;
}

/** Types summarized by name before "and more". */
const PENDING_TYPES_SHOWN = 3;

/**
 * Pending entries across one or more profiles' data, largest type first:
 * "12 plays · 3 ratings to add", or null when nothing would change.
 */
export function pendingSummary(
  data: readonly { id: ProfileDataTypeId; count: number; pending?: number }[],
): string | null {
  const totals = new Map<ProfileDataTypeId, number>();
  for (const entry of data) {
    const pending = pendingCount(entry);
    if (pending > 0) totals.set(entry.id, (totals.get(entry.id) ?? 0) + pending);
  }
  if (totals.size === 0) return null;
  const ordered = DATA_TYPE_IDS.filter((id) => totals.has(id)).sort(
    (a, b) => totals.get(b)! - totals.get(a)!,
  );
  const shown = ordered.slice(0, PENDING_TYPES_SHOWN).map((id) => {
    const count = totals.get(id)!;
    const [one, many] = PENDING_NOUNS[id];
    return `${count} ${count === 1 ? one : many}`;
  });
  const more = ordered.length > PENDING_TYPES_SHOWN ? " and more" : "";
  return `${shown.join(" · ")}${more} to add`;
}
