import { useMemo } from "react";
import type { LibraryCollection } from "@/api/types";
import { useAdminCollections } from "@/hooks/queries/admin/collections";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import {
  useAllUserCollections,
  type CollectionOption,
} from "@/hooks/queries/useAllUserCollections";
import type { RowCollections } from "@/lib/homeRows/types";

/**
 * The collections an admin row can show: every visible library collection on
 * the server, including ones in libraries the admin's own profile can't open.
 * Hidden collections are left out because a row could not show them.
 */
export function adminCollectionOptions(
  collections: readonly LibraryCollection[],
  libraries: ReadonlyArray<{ id: number; name: string }>,
): CollectionOption[] {
  const names = new Map(libraries.map((library) => [library.id, library.name]));
  return collections
    .filter((collection) => collection.visibility === "visible")
    .map((collection) => {
      const libraryName = names.get(collection.library_id) ?? "";
      return {
        id: collection.id,
        title: collection.title,
        source: "library",
        group: libraryName,
        library_id: collection.library_id,
        library_name: libraryName,
        collection_type: collection.collection_type,
        item_count: collection.item_count,
        poster_url: collection.poster_url,
        poster_thumbhash: collection.poster_thumbhash,
      };
    });
}

/** The admin Home rows collection picker's options. */
export function useAdminRowCollections(): RowCollections {
  const collections = useAdminCollections();
  const { data: libraries } = useAdminLibraries();
  const options = useMemo(
    () => adminCollectionOptions(collections.data ?? [], libraries ?? []),
    [collections.data, libraries],
  );
  return {
    options,
    loading: collections.isLoading,
    fetching: collections.isFetching,
    failed: collections.isError,
    href: "/admin/collections",
  };
}

/**
 * The Settings > Home Screen collection picker's options: this profile's own
 * collections and the library collections it can open, each with its own
 * poster (#1702 builds personal posters only from titles the viewer can open).
 */
export function useProfileRowCollections(): RowCollections {
  const { collections, isLoading, isFetching, isError } = useAllUserCollections();
  return {
    options: collections,
    loading: isLoading,
    fetching: isFetching,
    failed: isError,
    href: "/collections",
  };
}
