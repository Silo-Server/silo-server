import {
  deleteAdminSection,
  fetchAdminSectionOrderSnapshot,
  fetchAdminSectionSnapshot,
  updateAdminSection,
} from "@/api/adminSections";
import {
  collectionRefsOf,
  libraryCollectionIdsOf,
  withoutLibraryCollection,
} from "@/lib/homeRows/collectionList";
import { fetchAdminCollectionSnapshot } from "@/api/adminCollections";
import { holdsCollections } from "@/lib/sectionTypes";
import type { components } from "@/api/v2/schema";
import { v2, V2ProblemError } from "@/api/v2/request";
import type { CollectionRow } from "@/lib/collections/rows";
import { stableJson } from "@/lib/homeRows/stableJson";
import type { PageRef } from "@/lib/homeRows/types";

type AdminCollectionSection = components["schemas"]["AdminCollectionSection"];

function rowPage(value: AdminCollectionSection): PageRef {
  return value.scope === "library" && value.library_id !== null
    ? { kind: "library", libraryId: Number(value.library_id) }
    : { kind: "home" };
}

function pageKey(page: PageRef) {
  return page.kind === "home" ? "home" : String(page.libraryId);
}

/**
 * The administrator Home and library page rows that show a server collection.
 * Stored positions skip numbers once a row is deleted, so each row's place is
 * its rank in its page's order, read alongside.
 */
export async function fetchAdminCollectionRows(
  id: string,
  signal?: AbortSignal,
): Promise<CollectionRow[]> {
  const { items } = await v2("GET /api/v2/admin/collections/{id}/sections", {
    path: { id },
    signal,
  });
  const pages = new Map(items.map((item) => [pageKey(rowPage(item)), rowPage(item)]));
  const orders = new Map(
    await Promise.all(
      [...pages].map(async ([key, page]) => {
        const { ordered_ids } = await fetchAdminSectionOrderSnapshot(
          page.kind,
          page.kind === "library" ? page.libraryId : undefined,
          signal,
        );
        return [key, ordered_ids] as const;
      }),
    ),
  );
  return items.map((item) => {
    const page = rowPage(item);
    return {
      id: item.id,
      page,
      title: item.title,
      enabled: item.enabled,
      ...rowPlace(item, orders.get(pageKey(page)) ?? []),
      ...(item.collection_count ? { listCount: item.collection_count } : {}),
    };
  });
}

/** The row's place on its page, counting from 1, and how many rows the page has. */
function rowPlace(item: AdminCollectionSection, order: readonly string[]) {
  const rank = order.indexOf(item.id);
  if (rank >= 0) return { position: rank + 1, pageRowCount: order.length };
  // Deleted since the list was read: the server's own numbers are all there is.
  const pageRowCount = Math.max(item.page_row_count, 1);
  return { position: Math.min(item.position + 1, pageRowCount), pageRowCount };
}

function isGone(error: unknown) {
  return error instanceof V2ProblemError && error.status === 404;
}

/**
 * Takes collection `collectionId` off the `rows` that show it, one at a time,
 * each with the ETag of a fresh read: a row that shows only it is deleted,
 * and a row of collections that lists others too keeps them and loses only
 * this one. `rows` can be stale: a row that is already gone, or that no
 * longer shows the collection, is left alone and counts as done (the
 * collection's own delete still refuses if a row uses it). The first failure
 * stops the run: `remaining` holds that row and every row after it, with the
 * error.
 *
 * Deleting or changing a row that shows the collection moves the
 * collection's revision (a section write locks the collections it
 * referenced), so `etag`, the collection's ETag read before the run, would
 * answer `412` on the collection's own `DELETE`. When every row is done and
 * at least one was written, the answer's `etag` is the collection's ETag read
 * again; send that one. It is left out when the collection itself changed
 * since `etag` was read, so the `DELETE` still answers `412` to someone
 * else's edit. When it no longer carries `etag` before the run, or can't be
 * read, no row is written and every row is `remaining`.
 */
export async function deleteAdminCollectionRows(
  collectionId: string,
  rows: readonly CollectionRow[],
  etag: string,
): Promise<{ remaining: CollectionRow[]; error?: unknown; etag?: string }> {
  // The collection before any row is touched, to tell this run's own change
  // of its revision from someone else's edit. A row can't come back, so no
  // row is written unless the collection still carries `etag`.
  let before: CollectionSnapshot;
  try {
    before = await fetchAdminCollectionSnapshot(collectionId);
  } catch (error) {
    return { remaining: [...rows], error };
  }
  if (before.etag !== etag) return { remaining: [...rows], error: new CollectionChangedError() };
  let wroteRow = false;
  for (const [index, row] of rows.entries()) {
    try {
      const { section, etag: rowEtag } = await fetchAdminSectionSnapshot(row.id);
      const config = section.config ?? {};
      if (holdsCollections(section.section_type)) {
        const listed = libraryCollectionIdsOf(config);
        if (!listed.includes(collectionId)) continue;
        if (collectionRefsOf(config).length > 1) {
          await updateAdminSection({
            id: row.id,
            etag: rowEtag,
            config: withoutLibraryCollection(config, collectionId),
          });
          wroteRow = true;
          continue;
        }
      } else if (config.library_collection_id !== collectionId) {
        continue;
      }
      await deleteAdminSection({ id: row.id, etag: rowEtag });
      wroteRow = true;
    } catch (error) {
      if (isGone(error)) continue;
      return { remaining: rows.slice(index), error };
    }
  }
  if (!wroteRow) return { remaining: [] };
  try {
    const after = await fetchAdminCollectionSnapshot(collectionId);
    return sameCollection(before, after) ? { remaining: [], etag: after.etag } : { remaining: [] };
  } catch (error) {
    // The rows are done; the collection's delete reports what is wrong.
    if (isGone(error)) return { remaining: [] };
    throw error;
  }
}

/** The collection changed after the delete was confirmed; no row was written. */
class CollectionChangedError extends Error {}

type CollectionSnapshot = Awaited<ReturnType<typeof fetchAdminCollectionSnapshot>>;

/**
 * Whether two reads of a collection show the same collection. Its image URLs
 * are signed per read, so they are left out; the thumbhashes stand for the
 * images.
 */
function sameCollection(a: CollectionSnapshot, b: CollectionSnapshot) {
  const comparable = ({ collection }: CollectionSnapshot) =>
    stableJson({ ...collection, poster_url: undefined, backdrop_url: undefined });
  return comparable(a) === comparable(b);
}
