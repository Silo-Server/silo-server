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

export const COUNT_UNAVAILABLE = "Count unavailable";

/** Undefined is for a view that has not loaded a collection yet. */
export function collectionCountLabel(count: number | null | undefined): string | undefined {
  if (count === undefined) return undefined;
  return count === null ? COUNT_UNAVAILABLE : `${count} title${count === 1 ? "" : "s"}`;
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
