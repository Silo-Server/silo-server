import type { components } from "@/api/v2/schema";
import { t, type MessageKey } from "@/i18n";

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
  get watch_history() {
    return t("web.profiles.dataTypes.watchHistory");
  },
  get ratings() {
    return t("web.profiles.dataTypes.ratings");
  },
  get favorites() {
    return t("web.profiles.dataTypes.favorites");
  },
  get collections() {
    return t("web.profiles.dataTypes.collections");
  },
  get dismissals() {
    return t("web.profiles.dataTypes.dismissals");
  },
  get preferences() {
    return t("web.profiles.dataTypes.preferences");
  },
  get notifications() {
    return t("web.profiles.dataTypes.notifications");
  },
};

/** The same, inside a sentence (each read gives the interface language). */
export const DATA_TYPE_PHRASES: Record<ProfileDataTypeId, string> = {
  get watch_history() {
    return t("web.admin.backups.dataTypes.phrase.watchHistory");
  },
  get ratings() {
    return t("web.admin.backups.dataTypes.phrase.ratings");
  },
  get favorites() {
    return t("web.admin.backups.dataTypes.phrase.favorites");
  },
  get collections() {
    return t("web.admin.backups.dataTypes.phrase.collections");
  },
  get dismissals() {
    return t("web.admin.backups.dataTypes.phrase.dismissals");
  },
  get preferences() {
    return t("web.admin.backups.dataTypes.phrase.preferences");
  },
  get notifications() {
    return t("web.admin.backups.dataTypes.phrase.notifications");
  },
};

/** One data type's merge counts: "3 added, 1 updated, 10 already here, 2 left out". */
export function dataCountsPhrase(data: {
  added: number;
  updated: number;
  unchanged: number;
  left_out: number;
}): string {
  const parts = [
    data.added > 0 ? t("web.profiles.dataTypes.counts.added", { count: data.added }) : null,
    data.updated > 0 ? t("web.profiles.dataTypes.counts.updated", { count: data.updated }) : null,
    data.unchanged > 0
      ? t("web.profiles.dataTypes.counts.unchanged", { count: data.unchanged })
      : null,
    data.left_out > 0 ? t("web.profiles.dataTypes.counts.leftOut", { count: data.left_out }) : null,
  ].filter((part): part is string => part !== null);
  return parts.length > 0 ? parts.join(", ") : t("web.profiles.dataTypes.counts.nothing");
}

/** The most titles listed by name before "and N more". */
const TITLES_SHOWN = 10;

/** Titles an import or restore left out: "A, B, C, and 12 more". */
export function titleList(titles: readonly string[]): string {
  const shown = titles.slice(0, TITLES_SHOWN).join(", ");
  const rest = titles.length - TITLES_SHOWN;
  return rest > 0
    ? t("web.profiles.dataTypes.titleListMore", { titles: shown, count: rest })
    : shown;
}

/** Counts of pending entries per data type ("12 plays"). */
const PENDING_COUNTS: Record<ProfileDataTypeId, MessageKey> = {
  watch_history: "web.admin.backups.dataTypes.pending.watchHistory",
  ratings: "web.admin.backups.dataTypes.pending.ratings",
  favorites: "web.admin.backups.dataTypes.pending.favorites",
  collections: "web.admin.backups.dataTypes.pending.collections",
  dismissals: "web.admin.backups.dataTypes.pending.dismissals",
  preferences: "web.admin.backups.dataTypes.pending.preferences",
  notifications: "web.admin.backups.dataTypes.pending.notifications",
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
    return t(PENDING_COUNTS[id], { count, count_text: count });
  });
  const items = shown.join(" · ");
  return ordered.length > PENDING_TYPES_SHOWN
    ? t("web.admin.backups.dataTypes.pending.toAddMore", { items })
    : t("web.admin.backups.dataTypes.pending.toAdd", { items });
}
