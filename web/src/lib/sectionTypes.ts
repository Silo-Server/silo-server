export const FILTER_SECTION_TYPES = new Set(["genre", "custom_filter"]);

/** A row of collections: its cards are collections, not titles. */
export const COLLECTION_POSTERS_TYPE = "collection_posters";

/** Whether rows of this type show collections rather than titles. */
export function holdsCollections(sectionType: string): boolean {
  return sectionType === COLLECTION_POSTERS_TYPE;
}
