import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { deleteAdminCollectionRows, fetchAdminCollectionRows } from "@/api/adminCollectionRows";
import type { CollectionRow } from "@/lib/collections/rows";
import { adminKeys, sectionKeys } from "../keys";

/**
 * The admin Home and library page rows that show a server collection. Read
 * fresh each time it's asked for, because Home rows changes them elsewhere.
 * Pass no id (or `enabled: false`) when the server doesn't report rows.
 */
export function useAdminCollectionRows(collectionId: string | undefined, enabled = true) {
  return useQuery({
    queryKey: adminKeys.collectionRows(collectionId ?? ""),
    queryFn: ({ signal }) => fetchAdminCollectionRows(collectionId!, signal),
    enabled: enabled && Boolean(collectionId),
    staleTime: 0,
  });
}

/**
 * Deletes the rows that show a collection, one at a time, before the
 * collection itself is deleted (see `deleteAdminCollectionRows`). Home rows
 * and the collections' row counts are read again afterwards, whatever happened.
 */
export function useDeleteCollectionRows() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      collectionId,
      rows,
    }: {
      collectionId: string;
      rows: readonly CollectionRow[];
    }) => deleteAdminCollectionRows(collectionId, rows),
    onSettled: () =>
      Promise.all([
        queryClient.invalidateQueries({ queryKey: sectionKeys.all }),
        queryClient.invalidateQueries({ queryKey: ["admin", "collections"] }),
      ]),
  });
}
