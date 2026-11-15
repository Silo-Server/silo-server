import { useCallback, useMemo } from "react";
import { useQueries } from "@tanstack/react-query";

import { useUserLibraries } from "@/hooks/queries/libraries";
import { libraryCollectionTitlesQuery } from "@/hooks/queries/libraryCollections";
import { libraryLayoutQuery } from "@/hooks/queries/sections";
import { useSectionTitle } from "@/lib/sectionTitle";
import { useCriticalContentLoaded } from "@/lib/startupPriority";
import type { CollectionMenuItem, PrimaryMenuItem, SectionMenuItem } from "@/lib/uiCustomization";

/** A pinned library row or collection, with the label saved when it was pinned. */
export interface PinRef {
  type: "section" | "collection";
  id: string;
  /** Absent on a personal collection, which belongs to no library. */
  libraryId?: number;
  label: string;
}

/** The pin a menu link or shortcut points at, or null for a library or built-in link. */
export function menuItemPinRef(item: SectionMenuItem | CollectionMenuItem): PinRef;
export function menuItemPinRef(item: PrimaryMenuItem): PinRef | null;
export function menuItemPinRef(item: PrimaryMenuItem): PinRef | null {
  if (item.type === "section") {
    return { type: "section", id: item.section_id, libraryId: item.library_id, label: item.label };
  }
  if (item.type === "collection") {
    return {
      type: "collection",
      id: item.collection_id,
      libraryId: item.library_id,
      label: item.label,
    };
  }
  return null;
}

function visiblePinLibraryIds(
  pins: readonly PinRef[],
  type: PinRef["type"],
  libraryIds: readonly number[],
): number[] {
  const wanted = new Set(pins.flatMap((pin) => (pin.type === type ? [pin.libraryId] : [])));
  return libraryIds.filter((id) => wanted.has(id));
}

/**
 * Names pins by their current title instead of the label saved when they were
 * pinned: a library row by its title in the library's layout, translated like
 * the row itself, and a library collection by its title in the library's
 * Collections tab. The saved label shows until those load, when a read fails,
 * when the row or collection is gone, and for a personal collection.
 *
 * The layout read shares the library page's query. Both wait for the page's
 * first content (see startupPriority), and media changes refetch neither.
 */
export function usePinLabel(pins: readonly PinRef[]): (pin: PinRef) => string {
  const { data: libraries } = useUserLibraries();
  const criticalContentLoaded = useCriticalContentLoaded();
  const libraryIds = useMemo(() => (libraries ?? []).map((library) => library.id), [libraries]);
  const sectionLibraryIds = useMemo(
    () => visiblePinLibraryIds(pins, "section", libraryIds),
    [libraryIds, pins],
  );
  const collectionLibraryIds = useMemo(
    () => visiblePinLibraryIds(pins, "collection", libraryIds),
    [libraryIds, pins],
  );
  const layouts = useQueries({
    queries: sectionLibraryIds.map((libraryId) => ({
      ...libraryLayoutQuery(libraryId),
      enabled: criticalContentLoaded,
    })),
    combine: (results) => results.map((result) => result.data),
  });
  const collectionTitles = useQueries({
    queries: collectionLibraryIds.map((libraryId) => ({
      ...libraryCollectionTitlesQuery(libraryId),
      enabled: criticalContentLoaded,
    })),
    combine: (results) => results.map((result) => result.data),
  });
  const sectionTitle = useSectionTitle();
  return useCallback(
    (pin: PinRef) => {
      if (pin.libraryId === undefined) return pin.label;
      if (pin.type === "section") {
        const layout = layouts[sectionLibraryIds.indexOf(pin.libraryId)];
        const section = layout?.sections.find((entry) => entry.id === pin.id);
        return section ? sectionTitle(section) : pin.label;
      }
      const titles = collectionTitles[collectionLibraryIds.indexOf(pin.libraryId)];
      return titles?.get(pin.id) || pin.label;
    },
    [collectionLibraryIds, collectionTitles, layouts, sectionLibraryIds, sectionTitle],
  );
}
