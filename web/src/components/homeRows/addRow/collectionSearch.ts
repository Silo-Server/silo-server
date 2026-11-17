import { useMemo, useState } from "react";
import type { CollectionOption } from "@/hooks/queries/useAllUserCollections";
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
): string {
  if (collections.loading) return t("web.home.addRow.collection.loading");
  if (collections.failed) return t("web.home.addRow.collection.failed");
  if (collections.options.length === 0) return t("web.home.addRow.collection.none");
  return query
    ? t("web.home.addRow.collection.noMatch", { query })
    : t("web.home.addRow.collection.noneOfType");
}

/** The search text and type filter, and the options that pass both. */
export function useCollectionSearch(options: readonly CollectionOption[]) {
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<CollectionFilter>("all");
  const needle = query.trim().toLowerCase();
  const shown = useMemo(
    () =>
      options.filter(
        (option) =>
          (filter === "all" ||
            (!!option.collection_type && collectionKindOf(option.collection_type) === filter)) &&
          option.title.toLowerCase().includes(needle),
      ),
    [options, filter, needle],
  );
  return { query, setQuery, filter, setFilter, shown };
}

/** A collection's type and title count: "Smart · 12 titles". */
export function collectionOptionMeta(
  option: Pick<CollectionOption, "collection_type" | "item_count">,
): string {
  return [
    collectionKind(option.collection_type),
    option.item_count === undefined ? undefined : titleCount(option.item_count),
  ]
    .filter(Boolean)
    .join(" · ");
}
