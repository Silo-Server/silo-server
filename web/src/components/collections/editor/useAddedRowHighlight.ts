import { useEffect, useRef } from "react";

import { useNewRowHighlight } from "@/components/homeRows/useNewRowHighlight";
import type { CollectionRow } from "@/lib/collections/rows";

/**
 * Back from Add row in Home rows: the row just added (`addedRowId`, from the
 * navigation state) flashes once `rows` lists it, and only once.
 */
export function useAddedRowHighlight(
  addedRowId: string | undefined,
  rows: readonly CollectionRow[] | undefined,
): string | null {
  const listed = Boolean(addedRowId && rows?.some((row) => row.id === addedRowId));
  const [highlightId, setHighlightId] = useNewRowHighlight();
  const highlighted = useRef<string | undefined>(undefined);
  useEffect(() => {
    if (!listed || highlighted.current === addedRowId) return;
    highlighted.current = addedRowId;
    setHighlightId(addedRowId ?? null);
  }, [listed, addedRowId, setHighlightId]);
  return highlightId;
}
