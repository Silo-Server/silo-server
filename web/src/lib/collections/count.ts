import { t } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

/** A missing status is the legacy contract. Explicit future statuses fail closed. */
export interface CollectionCountFields {
  item_count?: number;
  item_count_status?: string;
}

/** Null means the count is unavailable; it says nothing about collection membership. */
export function collectionItemCount(collection: CollectionCountFields): number | null {
  if (collection.item_count_status !== undefined && collection.item_count_status !== "available")
    return null;
  return collection.item_count ?? 0;
}

/** Undefined is for a view that has not loaded a collection yet. */
export function collectionCountLabel(count: number | null | undefined): string | undefined {
  if (count === undefined) return undefined;
  if (count === null) return t("web.collections.count.unavailable");
  return t("web.collections.metaLine.titleCount", {
    count,
    shown: count.toLocaleString(formatLocale()),
  });
}

/** Most titles first, with all unavailable counts after known counts (including zero). */
export function compareCollectionCounts(
  a: CollectionCountFields,
  b: CollectionCountFields,
): number {
  const left = collectionItemCount(a);
  const right = collectionItemCount(b);
  if (left === null) return right === null ? 0 : 1;
  if (right === null) return -1;
  return right - left;
}
