import { useEffect, useState } from "react";

import { useUserLibraries } from "@/hooks/queries/libraries";
import { useProfileCollectionRows } from "@/hooks/queries/profileCollectionRows";
import { useIntersectionObserver } from "@/hooks/useIntersectionObserver";
import {
  matchedLibraries,
  onRowsLine,
  rowPlaceInSentence,
  type RowsState,
} from "@/lib/collections/rows";
import type { PageRef } from "@/lib/homeRows/types";

import { RowsOnceCreated, RowsThatShowIt } from "./RowsThatShowIt";
import { useAddedRowHighlight } from "./useAddedRowHighlight";

interface PersonalRowsProps {
  /** The saved collection; undefined until it is created. */
  collectionId: string | undefined;
  /** The libraries the draft matches, whose pages Add as a row offers. */
  draftLibraryIds: readonly number[];
  /** A row Home Screen just added from here (navigation state). */
  addedRowId?: string;
  disabledReason?: string | null;
  /** Add as a row on `page`; `where` names it inside a sentence ("my Kids page"). */
  onAdd: (page: PageRef, where: string) => void;
  /** "On my Home and my Kids page" for the header, once the rows are read. */
  onLineChange: (line: string | null) => void;
}

/**
 * Where it shows on a personal collection: the rows on the viewer's own Home
 * and library pages that show it, and Add as a row (My Home, My *Library*
 * page for each library the draft matches). Before the collection is created
 * it is one line. Nothing is read until the panel comes near the screen; then
 * one page at a time per slot (see `useProfileCollectionRows`). It only links
 * into Settings > Home Screen and never changes a row itself.
 */
export function PersonalRowsThatShowIt(props: PersonalRowsProps) {
  const [seen, setSeen] = useState(false);
  const created = Boolean(props.collectionId);
  const observe = useIntersectionObserver({
    onIntersect: () => setSeen(true),
    enabled: created && !seen,
    rootMargin: "200px",
  });
  if (!created) return <RowsOnceCreated mine />;
  if (!seen) return <div ref={observe} className="min-h-12" />;
  return <PersonalRows {...props} />;
}

function PersonalRows({
  collectionId,
  draftLibraryIds,
  addedRowId,
  disabledReason,
  onAdd,
  onLineChange,
}: PersonalRowsProps) {
  const libraries = useUserLibraries();
  // Until the display preferences load, the list still holds hidden libraries.
  const visible = libraries.isLoading ? undefined : libraries.data;
  // Every page, not only the matched ones: Home Screen offers a personal
  // collection on any page, and its libraries can change after a row is added.
  const pages: PageRef[] | undefined = visible && [
    { kind: "home" },
    ...visible.map((library) => ({ kind: "library" as const, libraryId: library.id })),
  ];
  const read = useProfileCollectionRows(collectionId, pages);
  const rows: RowsState =
    libraries.isError && !visible
      ? { status: "error", onRetry: () => void libraries.refetch() }
      : read;
  const listed = rows.status === "ready" ? rows.rows : undefined;
  const names = new Map((visible ?? []).map((library) => [library.id, library.name]));

  const line = listed ? onRowsLine(listed, names) : null;
  useEffect(() => onLineChange(line), [line, onLineChange]);
  const highlightId = useAddedRowHighlight(addedRowId, listed);

  return (
    <RowsThatShowIt
      rows={rows}
      libraryNames={names}
      highlightId={highlightId}
      mine
      addAsRow={{
        bound: matchedLibraries(visible ?? [], draftLibraryIds),
        others: [],
        disabledReason,
        onPick: (page) => onAdd(page, rowPlaceInSentence(page, names, "profile")),
      }}
    />
  );
}
