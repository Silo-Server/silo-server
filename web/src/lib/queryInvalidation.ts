export function activeCatalogQueryMatchesLibrary(queryKey: unknown, libraryId?: number) {
  if (!libraryId || !Array.isArray(queryKey)) return true;
  if (queryKey[0] !== "catalog" || queryKey[1] !== "list") return true;
  const params = queryKey[2] as { library_id?: number } | undefined;
  return params?.library_id == null || params.library_id === libraryId;
}

/**
 * Decides whether a `["sections", …]` query belongs to the library an event
 * changed.
 *
 * Home sections are deliberately excluded: they are not library-scoped, so a
 * scan touching one library used to invalidate every home row on every event.
 * Home rerenders from cache and refreshes on its next mount, which is cheap;
 * refetching the whole home layout thousands of times during a scan is not.
 */
export function activeSectionQueryMatchesLibrary(queryKey: unknown, libraryId?: number) {
  if (!libraryId || !Array.isArray(queryKey)) return true;
  if (queryKey[0] !== "sections") return true;
  if (queryKey[1] === "home") return false;
  if (queryKey[1] !== "library") return true;
  const queryLibraryId = queryKey[2];
  return queryLibraryId == null || queryLibraryId === libraryId;
}
