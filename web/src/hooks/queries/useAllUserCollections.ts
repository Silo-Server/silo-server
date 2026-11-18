import { useQueries } from "@tanstack/react-query";
import type { Collection, LibraryCollection } from "@/api/types";
import { useUserLibraries } from "./libraries";
import { useCollections } from "./collections";
import { getLibraryCollectionList, libraryCollectionsQueryOptions } from "./libraryCollections";

export interface CollectionOption {
  id: string;
  title: string;
  source: "library" | "user";
  /** On a personal collection: the profile that made it; others may share theirs. */
  creator_profile_id?: string;
  group: string;
  library_id?: number;
  library_name?: string;
  /** Every library that shows this collection; the pickers filter by these. */
  libraries?: CollectionLibrary[];
  collection_type?: LibraryCollection["collection_type"];
  last_sync_status?: LibraryCollection["last_sync_status"];
  item_count?: number;
  poster_url?: string;
  poster_thumbhash?: string;
}

export interface CollectionLibrary {
  id: number;
  name: string;
}

type UserCollectionSummary = Pick<Collection, "id" | "name"> &
  Partial<
    Pick<
      Collection,
      "creator_profile_id" | "collection_type" | "item_count" | "poster_url" | "poster_thumbhash"
    >
  >;

function buildAllUserCollectionOptions(
  libraries: readonly CollectionLibrary[],
  userCollections: readonly UserCollectionSummary[] | undefined,
  libraryCollectionsByLibrary: ReadonlyArray<readonly LibraryCollection[] | undefined>,
): CollectionOption[] {
  const collections: CollectionOption[] = [];

  for (const collection of userCollections ?? []) {
    collections.push({
      id: collection.id,
      title: collection.name,
      source: "user",
      creator_profile_id: collection.creator_profile_id,
      group: "My Collections",
      collection_type: collection.collection_type,
      item_count: collection.item_count,
      poster_url: collection.poster_url,
      poster_thumbhash: collection.poster_thumbhash,
    });
  }

  type LibraryOption = CollectionOption & { libraries: CollectionLibrary[] };
  const libraryOptions = new Map<string, LibraryOption>();

  for (let i = 0; i < libraries.length; i++) {
    const library = libraries[i]!;
    for (const collection of libraryCollectionsByLibrary[i] ?? []) {
      const existing = libraryOptions.get(collection.id);
      if (existing) {
        if (!existing.libraries.some((shown) => shown.id === library.id)) {
          existing.libraries.push({ id: library.id, name: library.name });
          const group = existing.libraries.map((shown) => shown.name).join(", ");
          existing.group = group;
          existing.library_name = group;
        }
        continue;
      }

      const option: LibraryOption = {
        id: collection.id,
        title: collection.title,
        source: "library",
        group: library.name,
        library_id: library.id,
        library_name: library.name,
        libraries: [{ id: library.id, name: library.name }],
        collection_type: collection.collection_type,
        last_sync_status: collection.last_sync_status,
        item_count: collection.item_count,
        poster_url: collection.poster_url,
        poster_thumbhash: collection.poster_thumbhash,
      };
      libraryOptions.set(collection.id, option);
      collections.push(option);
    }
  }

  return collections;
}

export function useAllUserCollections() {
  const {
    data: libraries,
    isLoading: librariesLoading,
    isFetching: librariesFetching,
    isError: librariesFailed,
  } = useUserLibraries();
  const {
    data: userCollections,
    isLoading: userCollectionsLoading,
    isFetching: userCollectionsFetching,
    isError: userCollectionsFailed,
  } = useCollections();

  const libraryQueries = useQueries({
    queries: (libraries ?? []).map((lib) => ({
      ...libraryCollectionsQueryOptions(lib.id),
      select: getLibraryCollectionList,
    })),
  });

  // Until the libraries load, their collections aren't even asked for.
  const isLoading =
    librariesLoading || libraryQueries.some((q) => q.isLoading) || userCollectionsLoading;
  const isFetching =
    librariesFetching || libraryQueries.some((q) => q.isFetching) || userCollectionsFetching;
  const isError = librariesFailed || libraryQueries.some((q) => q.isError) || userCollectionsFailed;

  const libraryCollectionsByLibrary = libraryQueries.map((result) =>
    Array.isArray(result.data) ? result.data : undefined,
  );
  const collections = buildAllUserCollectionOptions(
    libraries ?? [],
    userCollections,
    libraryCollectionsByLibrary,
  );

  return { collections, isLoading, isFetching, isError };
}
