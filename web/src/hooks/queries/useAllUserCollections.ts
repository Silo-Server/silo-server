import { useQueries } from "@tanstack/react-query";
import type { LibraryCollection } from "@/api/types";
import { useUserLibraries } from "./libraries";
import { useCollections } from "./collections";
import { getLibraryCollectionList, libraryCollectionsQueryOptions } from "./libraryCollections";

export interface CollectionOption {
  id: string;
  title: string;
  source: "library" | "user";
  group: string;
  library_id?: number;
  library_name?: string;
  collection_type?: LibraryCollection["collection_type"];
  source_config?: LibraryCollection["source_config"];
  last_sync_status?: LibraryCollection["last_sync_status"];
}

export function useAllUserCollections() {
  const { data: libraries } = useUserLibraries();
  const { data: userCollections, isLoading: userCollectionsLoading } = useCollections();

  const libraryQueries = useQueries({
    queries: (libraries ?? []).map((lib) => ({
      ...libraryCollectionsQueryOptions(lib.id),
      select: getLibraryCollectionList,
    })),
  });

  const isLoading = libraryQueries.some((q) => q.isLoading) || userCollectionsLoading;

  const collections: CollectionOption[] = [];

  // Add personal user collections first (grouped under "My Collections").
  if (userCollections) {
    for (const c of userCollections) {
      collections.push({
        id: c.id,
        title: c.name,
        source: "user",
        group: "My Collections",
      });
    }
  }

  // Add library collections grouped by library name.
  if (libraries) {
    for (let i = 0; i < libraries.length; i++) {
      const lib = libraries[i]!;
      const result = libraryQueries[i];
      const libraryCollections = Array.isArray(result?.data) ? result.data : [];
      for (const c of libraryCollections) {
        collections.push({
          id: c.id,
          title: c.title,
          source: "library",
          group: lib.name,
          library_id: lib.id,
          library_name: lib.name,
          collection_type: c.collection_type,
          source_config: c.source_config,
          last_sync_status: c.last_sync_status,
        });
      }
    }
  }

  return { collections, isLoading };
}
