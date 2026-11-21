import { useQuery } from "@tanstack/react-query";

import type { SectionCollectionCard } from "@/api/types";
import {
  type CatalogCardItem,
  catalogItemFromV2,
  sectionCollectionCardFromV2,
} from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import { collectionKeys } from "@/hooks/queries/keys";

/**
 * The collections holding a title, and, when only one does, up to six of its
 * other titles; see listCatalogItemCollections.
 */
export interface ItemCollections {
  cards: SectionCollectionCard[];
  preview: CatalogCardItem[];
}

/** The collections a title's page shows; see listCatalogItemCollections. */
export async function fetchItemCollections(
  contentId: string,
  libraryId?: number,
  options?: Pick<RequestInit, "signal">,
): Promise<ItemCollections> {
  const result = await v2("GET /api/v2/catalog/items/{id}/collections", {
    path: { id: contentId },
    query: { library_id: libraryId !== undefined ? String(libraryId) : undefined },
    signal: options?.signal ?? undefined,
  });
  return {
    cards: result.items.map(sectionCollectionCardFromV2),
    preview: result.preview_items.map(catalogItemFromV2),
  };
}

/**
 * The collections holding the title a detail page shows. The key sits under
 * the collection keys, so adding the title to a collection, or any other
 * collection change, refreshes the row. A failure shows no row rather than an
 * error on the title's page.
 */
export function useItemCollections(contentId: string, libraryId?: number) {
  return useQuery({
    queryKey: collectionKeys.onTitle(contentId, libraryId),
    queryFn: ({ signal }) => fetchItemCollections(contentId, libraryId, { signal }),
    enabled: contentId !== "",
    retry: false,
  });
}
