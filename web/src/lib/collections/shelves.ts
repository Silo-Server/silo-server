/**
 * A library's shelves in Arrange: the server's collection groups plus the
 * collections on no shelf ("No heading"), in the order viewers see them on
 * the library's Collections tab, and the moves Arrange can make.
 */
import type { GroupSortMode, LibraryCollection, LibraryCollectionGroup } from "@/api/types";

import { NO_HEADING } from "./copy";

/** The id the order routes use for the collections on no shelf. */
export const UNGROUPED = "ungrouped";

export type ShelfKind = "regular" | "user_collections" | "ungrouped";

export interface Shelf {
  /** The group's id, or `UNGROUPED`. */
  id: string;
  kind: ShelfKind;
  name: string;
  sortMode: GroupSortMode;
  /** In the order the server stores them; `shownCollections` gives the order viewers see. */
  collections: LibraryCollection[];
}

export interface BoardGroup extends LibraryCollectionGroup {
  collections: LibraryCollection[];
}

/** A collection's new place: its shelf's whole stored order once the move saves. */
export interface CollectionMove {
  shelfId: string;
  orderedIds: string[];
}

export const SHELF_ORDER_LABEL: Readonly<Record<GroupSortMode, string>> = {
  manual: "Your order",
  name_asc: "Name A–Z",
  name_desc: "Name Z–A",
  recent: "Recently updated",
  most_items: "Most titles",
};

const SORTED_BY: Readonly<Record<GroupSortMode, string | null>> = {
  manual: null,
  name_asc: "name",
  name_desc: "name",
  recent: "recently updated",
  most_items: "most titles",
};

/** Plain string order, as the server compares ids and names (Go's `<`, not a locale). */
function compare(a: string, b: string) {
  return a < b ? -1 : a > b ? 1 : 0;
}

function byId(a: { id: string }, b: { id: string }) {
  return compare(a.id, b.id);
}

function move<T>(list: readonly T[], from: number, to: number): T[] {
  const next = [...list];
  const [moved] = next.splice(from, 1);
  next.splice(to, 0, moved!);
  return next;
}

/**
 * The shelves top to bottom. No heading sits at its own position; a tie in
 * position falls back to the id, its sentinel included, as the server orders them.
 */
export function boardShelves(
  groups: readonly BoardGroup[],
  ungrouped: readonly LibraryCollection[],
  ungroupedSortOrder: number,
): Shelf[] {
  const slots = [
    ...groups.map((group) => ({
      order: group.sort_order,
      shelf: {
        id: group.id,
        kind: group.kind,
        name: group.name,
        sortMode: group.default_sort_mode,
        collections: group.collections,
      } satisfies Shelf,
    })),
    {
      order: ungroupedSortOrder,
      shelf: {
        id: UNGROUPED,
        kind: "ungrouped",
        name: NO_HEADING,
        sortMode: "manual",
        collections: [...ungrouped],
      } satisfies Shelf,
    },
  ];
  slots.sort((a, b) => a.order - b.order || byId(a.shelf, b.shelf));
  return slots.map((slot) => slot.shelf);
}

/**
 * Pinned (`featured`) collections first, each part keeping its order. The
 * server lists a library's collections pinned first, and a shelf in Your
 * order keeps that order.
 */
function pinnedFirst<T extends { featured: boolean }>(list: readonly T[]): T[] {
  return [...list.filter((entry) => entry.featured), ...list.filter((entry) => !entry.featured)];
}

/** A shelf's collections in the order viewers see them, sorted as the server sorts them. */
export function shownCollections(shelf: Shelf): LibraryCollection[] {
  const list = [...shelf.collections];
  switch (shelf.sortMode) {
    case "name_asc":
      return list.sort((a, b) => compare(a.title, b.title));
    case "name_desc":
      return list.sort((a, b) => compare(b.title, a.title));
    case "recent":
      return list.sort((a, b) => Date.parse(b.updated_at) - Date.parse(a.updated_at));
    case "most_items":
      return list.sort((a, b) => (b.item_count ?? 0) - (a.item_count ?? 0));
    default:
      return pinnedFirst(list);
  }
}

/**
 * The pinned collections that lead a Your order shelf (No heading included).
 * A shelf that sorts itself ignores Pin, so it has no band.
 */
export function pinnedBand(shelf: Shelf): LibraryCollection[] {
  return shelf.sortMode === "manual" ? shelf.collections.filter((entry) => entry.featured) : [];
}

/** The shelves with collection `id` pinned or unpinned, to show a Pin before it saves. */
export function applyPin(shelves: readonly Shelf[], id: string, pinned: boolean): Shelf[] {
  return shelves.map((shelf) =>
    shelf.collections.some((entry) => entry.id === id)
      ? {
          ...shelf,
          collections: shelf.collections.map((entry) =>
            entry.id === id ? { ...entry, featured: pinned } : entry,
          ),
        }
      : shelf,
  );
}

/** What a shelf that orders itself sorts by ("name", "most titles"); null for Your order. */
export function sortedBy(mode: GroupSortMode): string | null {
  return SORTED_BY[mode];
}

/** "3 collections", or "2 collections, sorted by name" on a shelf that orders itself. */
export function shelfCountLine(shelf: Shelf): string {
  const count = shelf.collections.length;
  const line = `${count} collection${count === 1 ? "" : "s"}`;
  const by = sortedBy(shelf.sortMode);
  return by ? `${line}, sorted by ${by}` : line;
}

/** My collections holds each viewer's own collections, never a server one. */
export function acceptsCollections(shelf: Shelf): boolean {
  return shelf.kind !== "user_collections";
}

export function shelfOf(shelves: readonly Shelf[], collectionId: string): Shelf | undefined {
  return shelves.find((shelf) => shelf.collections.some((entry) => entry.id === collectionId));
}

/**
 * Where collection `id` goes when it's dropped on collection `overId` of
 * shelf `shelfId`, or on the shelf itself (`overId` null, also Move to shelf).
 * Within its shelf it takes the place of the card it lands on, as the drag
 * showed; from another shelf it goes before that card, or last. On a Your
 * order shelf the pinned band stays first: a card dropped on it lands right
 * after it, a pinned card stays in it, and the order saved is the order
 * shown. Null when nothing would change or the shelf can't take it.
 */
export function planCollectionMove(
  shelves: readonly Shelf[],
  id: string,
  shelfId: string,
  overId: string | null,
): CollectionMove | null {
  const target = shelves.find((shelf) => shelf.id === shelfId);
  if (!target || !acceptsCollections(target)) return null;
  const manual = target.sortMode === "manual";
  const list = manual ? shownCollections(target) : target.collections;
  const ids = list.map((entry) => entry.id);
  const from = ids.indexOf(id);
  let next: LibraryCollection[];
  if (from !== -1) {
    // Order within a shelf that sorts itself changes nothing viewers see.
    if (overId === null || !manual) return null;
    const to = ids.indexOf(overId);
    if (to === -1 || to === from) return null;
    next = move(list, from, to);
  } else {
    const moved = shelves.flatMap((shelf) => shelf.collections).find((entry) => entry.id === id);
    if (!moved) return null;
    const at = overId === null ? -1 : ids.indexOf(overId);
    next = at === -1 ? [...list, moved] : [...list.slice(0, at), moved, ...list.slice(at)];
  }
  const orderedIds = (manual ? pinnedFirst(next) : next).map((entry) => entry.id);
  return from !== -1 && sameOrder(orderedIds, ids) ? null : { shelfId, orderedIds };
}

function sameOrder(a: readonly string[], b: readonly string[]) {
  return a.length === b.length && a.every((id, index) => id === b[index]);
}

/** The shelves as they look once `next` saves. */
export function applyCollectionMove(
  shelves: readonly Shelf[],
  id: string,
  next: CollectionMove,
): Shelf[] {
  const moved = shelves.flatMap((shelf) => shelf.collections).find((entry) => entry.id === id);
  if (!moved) return [...shelves];
  return shelves.map((shelf) => {
    const rest = shelf.collections.filter((entry) => entry.id !== id);
    if (shelf.id !== next.shelfId)
      return rest.length === shelf.collections.length ? shelf : { ...shelf, collections: rest };
    const known = new Map([...rest, moved].map((entry) => [entry.id, entry]));
    return {
      ...shelf,
      collections: next.orderedIds.flatMap((entry) => known.get(entry) ?? []),
    };
  });
}

/** Every shelf's id once shelf `id` moves to index `to` (clamped), or null when it's already there. */
export function planShelfMove(shelves: readonly Shelf[], id: string, to: number): string[] | null {
  const ids = shelves.map((shelf) => shelf.id);
  const from = ids.indexOf(id);
  const target = Math.max(0, Math.min(to, ids.length - 1));
  return from === -1 || from === target ? null : move(ids, from, target);
}

export function applyShelfMove(shelves: readonly Shelf[], orderedIds: readonly string[]): Shelf[] {
  const known = new Map(shelves.map((shelf) => [shelf.id, shelf]));
  return orderedIds.flatMap((id) => known.get(id) ?? []);
}
