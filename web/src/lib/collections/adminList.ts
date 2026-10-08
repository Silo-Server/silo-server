/**
 * The server collections page (`/admin/collections`): what its URL holds and
 * which collections the List view shows for it.
 */
import type { LibraryCollection } from "@/api/types";

import { joinNames } from "./copy";
import { collectionKindOf, type CollectionKind } from "./types";

export type AdminListView = "list" | "arrange";
export type KindFilter = "all" | CollectionKind;

/** Everything the page keeps in its URL, so a reload or an editor's Back lands on the same view. */
export interface AdminListState {
  view: AdminListView;
  /** null: All libraries. */
  libraryId: number | null;
  kind: KindFilter;
  q: string;
  /** Only lists whose last sync failed. */
  failed: boolean;
}

const KINDS = new Set<string>(["manual", "smart", "synced"]);

export function readAdminListState(params: URLSearchParams): AdminListState {
  const library = Number(params.get("libraryId"));
  const libraryId = Number.isInteger(library) && library > 0 ? library : null;
  const view = params.get("view");
  const type = params.get("type") ?? "";
  return {
    // A bare `?libraryId=N` is how older links and editors opened a library's
    // board, so it still opens Arrange; the List always names its view.
    view: view === "arrange" || view === "list" ? view : libraryId ? "arrange" : "list",
    libraryId,
    kind: KINDS.has(type) ? (type as CollectionKind) : "all",
    q: params.get("q") ?? "",
    failed: params.get("failed") === "1",
  };
}

/** `current` with the page's parameters replaced by `state`; other parameters stay. */
export function writeAdminListState(
  current: URLSearchParams,
  state: AdminListState,
): URLSearchParams {
  const next = new URLSearchParams(current);
  for (const name of ["view", "libraryId", "type", "q", "failed"]) next.delete(name);
  if (state.view === "arrange" || state.libraryId !== null) next.set("view", state.view);
  if (state.libraryId !== null) next.set("libraryId", String(state.libraryId));
  if (state.kind !== "all") next.set("type", state.kind);
  if (state.q) next.set("q", state.q);
  if (state.failed) next.set("failed", "1");
  return next;
}

export function collectionLibraryIds(collection: LibraryCollection): number[] {
  return collection.library_ids.length > 0 ? collection.library_ids : [collection.library_id];
}

/** The collections the List shows for `state`, by name. */
export function filterAdminCollections(
  collections: readonly LibraryCollection[],
  state: Pick<AdminListState, "libraryId" | "kind" | "q" | "failed">,
): LibraryCollection[] {
  const query = state.q.trim().toLocaleLowerCase();
  return collections
    .filter(
      (collection) =>
        (state.libraryId === null || collectionLibraryIds(collection).includes(state.libraryId)) &&
        (state.kind === "all" || collectionKindOf(collection.collection_type) === state.kind) &&
        (!query || collection.title.toLocaleLowerCase().includes(query)) &&
        (!state.failed || collection.last_sync_status === "failed"),
    )
    .sort((a, b) => a.title.localeCompare(b.title));
}

export function countByLibrary(collections: readonly LibraryCollection[]): Map<number, number> {
  const counts = new Map<number, number>();
  for (const collection of collections) {
    for (const id of collectionLibraryIds(collection)) counts.set(id, (counts.get(id) ?? 0) + 1);
  }
  return counts;
}

/**
 * The one tag a row may carry. Hidden wins: a hidden collection's Home rows
 * can't open See all, which matters more than that they exist.
 */
export function rowTag(collection: LibraryCollection): "hidden" | "home" | null {
  if (collection.visibility === "hidden") return "hidden";
  return (collection.home_row_count ?? 0) > 0 ? "home" : null;
}

/** The row switch's name: what turning it on does, and where. */
export function showOnTabsLabel(name: string, libraryNames: readonly string[]): string {
  if (libraryNames.length === 0) return `Show ${name} on its Collections tabs`;
  return `Show ${name} on the ${joinNames(libraryNames)} Collections tab${libraryNames.length === 1 ? "" : "s"}`;
}
