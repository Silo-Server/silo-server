/**
 * A row of collections (section_type `collection_posters`): its config lists
 * the collections it shows, in order, each by one id key.
 * `{"collections":[{"library_collection_id":"…"},{"user_collection_id":"…"}]}`
 */
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { Surface } from "./types";

/** The most collections one row lists, as the server's recipe allows. */
export const MAX_ROW_COLLECTIONS = 30;

/** One collection a row lists. */
export interface CollectionRef {
  source: "library" | "user";
  id: string;
}

/** A ref's key, unique across server and personal collections. */
export function collectionRefKey(ref: CollectionRef): string {
  return `${ref.source}:${ref.id}`;
}

/** The key of the ref an option would add. */
export function optionRefKey(option: Pick<CollectionOption, "id" | "source">): string {
  return collectionRefKey({ source: option.source, id: option.id });
}

/** The collections a row's config lists, in order. Entries it can't read are skipped. */
export function collectionRefsOf(config: Record<string, unknown> | undefined): CollectionRef[] {
  const list = config?.collections;
  if (!Array.isArray(list)) return [];
  const refs: CollectionRef[] = [];
  for (const entry of list) {
    if (typeof entry !== "object" || entry === null) continue;
    const { library_collection_id: library, user_collection_id: user } = entry as Record<
      string,
      unknown
    >;
    if (typeof library === "string" && library) refs.push({ source: "library", id: library });
    else if (typeof user === "string" && user) refs.push({ source: "user", id: user });
  }
  return refs;
}

/** The config with `refs` as its list; every other key stays. */
export function withCollectionRefs(
  config: Record<string, unknown>,
  refs: readonly CollectionRef[],
): Record<string, unknown> {
  return {
    ...config,
    collections: refs.map((ref) =>
      ref.source === "user" ? { user_collection_id: ref.id } : { library_collection_id: ref.id },
    ),
  };
}

/**
 * Whether the server would take the list: at least one collection, at most
 * MAX_ROW_COLLECTIONS, and on the admin surface only server collections.
 */
export function canSaveCollectionRefs(refs: readonly CollectionRef[], surface: Surface): boolean {
  return (
    refs.length > 0 &&
    refs.length <= MAX_ROW_COLLECTIONS &&
    (surface === "profile" || refs.every((ref) => ref.source === "library"))
  );
}

/** The library collection ids a row of collections lists. */
export function libraryCollectionIdsOf(config: Record<string, unknown> | undefined): string[] {
  return collectionRefsOf(config)
    .filter((ref) => ref.source === "library")
    .map((ref) => ref.id);
}

/** The row's config without one server collection. */
export function withoutLibraryCollection(
  config: Record<string, unknown>,
  collectionId: string,
): Record<string, unknown> {
  return withCollectionRefs(
    config,
    collectionRefsOf(config).filter(
      (ref) => !(ref.source === "library" && ref.id === collectionId),
    ),
  );
}
