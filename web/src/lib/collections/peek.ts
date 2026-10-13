/**
 * Poster peeks on the server collections list: a row's first titles, read
 * the way a viewer's Collections tab reads them, so they only ever show
 * titles the acting profile may see.
 */
import type { LibraryCollection } from "@/api/types";
import { isNotFoundProblem, v2 } from "@/api/v2/request";
import type { PeekRequest, PreviewItem } from "@/components/calm/usePeekLimiter";

/** Titles a peek asks for. Phones show the first two. */
export const COLLECTION_PEEK_LIMIT = 3;

/** The collection's own poster: what a row shows before its titles load, and when they can't. */
function ownPoster(collection: LibraryCollection): PreviewItem[] {
  if (!collection.poster_url && !collection.poster_thumbhash) return [];
  return [
    {
      id: collection.id,
      title: collection.title,
      posterUrl: collection.poster_url || undefined,
      thumbhash: collection.poster_thumbhash || undefined,
    },
  ];
}

/**
 * A row's peek on `libraryId`'s Collections tab. A hidden collection isn't on
 * any tab, so its peek is its own poster and sends no request. `hidden` is
 * what the row shows, which leads the stored visibility while a change is in
 * flight. The key moves with `updated_at`, so a changed collection peeks again.
 */
export function serverCollectionPeek(
  collection: LibraryCollection,
  libraryId: number,
  hidden: boolean,
): PeekRequest {
  const poster = ownPoster(collection);
  return {
    queryKey: [
      "collection-peek",
      "server",
      collection.id,
      libraryId,
      hidden,
      collection.updated_at,
    ],
    fetch: async (signal) => {
      if (hidden) return poster;
      try {
        const page = await v2("GET /api/v2/library/{id}/collections/{collection_id}/items", {
          path: { id: String(libraryId), collection_id: collection.id },
          query: { limit: COLLECTION_PEEK_LIMIT },
          signal,
        });
        const items = page.items.map((item) => ({
          id: item.content_id,
          title: item.title,
          posterUrl: item.poster_url || undefined,
          thumbhash: item.poster_thumbhash || undefined,
        }));
        return items.length > 0 ? items : poster;
      } catch (error) {
        // Gone or out of this profile's reach since the list loaded.
        if (isNotFoundProblem(error)) return poster;
        throw error;
      }
    },
    placeholder: () => (poster.length > 0 ? poster : undefined),
  };
}
