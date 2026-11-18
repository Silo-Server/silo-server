import { useMemo, useState } from "react";
import type { CollectionLibrary, CollectionOption } from "@/hooks/queries/useAllUserCollections";
import type { Translate } from "@/i18n";
import { collectionKindOf, type CollectionKind } from "@/lib/collections/types";
import { collectionKind, titleCount } from "@/lib/homeRows/describe";
import type { RowCollections } from "@/lib/homeRows/types";

/** The collection pickers' type filter: every collection, or one kind. */
export type CollectionFilter = "all" | CollectionKind;

/** Why the list shows no collection: loading, failed, none at all, or none that match. */
export function collectionEmptyText(
  t: Translate,
  collections: RowCollections,
  query: string,
  libraryPicked: boolean,
): string {
  if (collections.loading) return t("web.home.addRow.collection.loading");
  if (collections.failed) return t("web.home.addRow.collection.failed");
  if (collections.options.length === 0) return t("web.home.addRow.collection.none");
  if (query) return t("web.home.addRow.collection.noMatch", { query });
  return libraryPicked
    ? t("web.home.addRow.collection.noneInLibrary")
    : t("web.home.addRow.collection.noneOfType");
}

/** The pickers' library filter: every collection, or one library's by its id. */
export type CollectionLibraryFilter = "all" | number;

/**
 * The search text, type filter and library filter, and the options that pass
 * all three. `libraries` lists the libraries that show any option, by name;
 * the library filter falls back to every library once its library has no
 * option left or fewer than two libraries remain.
 */
export function useCollectionSearch(options: readonly CollectionOption[]) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<CollectionFilter>("all");
  const [pickedLibrary, setLibrary] = useState<CollectionLibraryFilter>("all");
  const libraries = useMemo(() => librariesOf(options), [options]);
  // The bar offers the library filter only for two or more libraries; with
  // fewer, a pick made earlier must not keep hiding collections.
  const library =
    libraries.length > 1 && libraries.some((entry) => entry.id === pickedLibrary)
      ? pickedLibrary
      : "all";
  // Forget a pick that stopped applying, so it can't return with its library.
  if (library !== pickedLibrary) setLibrary(library);
  const needle = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      options.filter(
        (option) =>
          (filter === "all" ||
            (!!option.collection_type && collectionKindOf(option.collection_type) === filter)) &&
          (library === "all" || !!option.libraries?.some((entry) => entry.id === library)) &&
          option.title.toLowerCase().includes(needle),
      ),
    [options, filter, library, needle],
  );
  return { query, setQuery, filter, setFilter, library, setLibrary, libraries, shown };
}

function librariesOf(options: readonly CollectionOption[]): CollectionLibrary[] {
  const byId = new Map<number, CollectionLibrary>();
  for (const option of options) {
    for (const library of option.libraries ?? []) {
      // A library whose name hasn't loaded yet can't be offered by name.
      if (library.name && !byId.has(library.id)) byId.set(library.id, library);
    }
  }
  return [...byId.values()].sort((a, b) => a.name.localeCompare(b.name));
}

/**
 * A collection's library, type and title count: "Movies · Smart · 12 titles".
 * The library tells apart collections of the same name in several libraries.
 */
export function collectionOptionMeta(
  option: Pick<CollectionOption, "library_name" | "collection_type" | "item_count">,
): string {
  return [
    option.library_name || undefined,
    collectionKind(option.collection_type),
    option.item_count === undefined ? undefined : titleCount(option.item_count),
  ]
    .filter(Boolean)
    .join(" · ");
}
