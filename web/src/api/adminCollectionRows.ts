import {
  deleteAdminSection,
  fetchAdminSectionOrderSnapshot,
  fetchAdminSectionSnapshot,
} from "@/api/adminSections";
import type { components } from "@/api/v2/schema";
import { v2, V2ProblemError } from "@/api/v2/request";
import type { CollectionRow } from "@/lib/collections/rows";
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
 * Deletes the `rows` that show collection `collectionId`, one at a time, each
 * with the ETag of a fresh read. `rows` can be stale: a row that is already
 * gone, or that now shows another collection, is left alone and counts as
 * done (the collection's own delete still refuses if a row uses it). The
 * first failure stops the run: `remaining` holds that row and every row after
 * it, with the error.
 */
export async function deleteAdminCollectionRows(
  collectionId: string,
  rows: readonly CollectionRow[],
): Promise<{ remaining: CollectionRow[]; error?: unknown }> {
  for (const [index, row] of rows.entries()) {
    try {
      const { section, etag } = await fetchAdminSectionSnapshot(row.id);
      if (section.config?.library_collection_id !== collectionId) continue;
      await deleteAdminSection({ id: row.id, etag });
    } catch (error) {
      if (isGone(error)) continue;
      return { remaining: rows.slice(index), error };
    }
  }
  return { remaining: [] };
}
