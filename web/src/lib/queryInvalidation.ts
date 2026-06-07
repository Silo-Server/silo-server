export function activeCatalogQueryMatchesLibrary(queryKey: unknown, libraryId?: number) {
  if (!libraryId || !Array.isArray(queryKey)) return true;
  if (queryKey[0] !== "catalog" || queryKey[1] !== "list") return true;
  const params = queryKey[2] as { library_id?: number } | undefined;
  return params?.library_id == null || params.library_id === libraryId;
}

export function activeSectionQueryMatchesLibrary(queryKey: unknown, libraryId?: number) {
  if (!libraryId || !Array.isArray(queryKey)) return true;
  if (queryKey[0] !== "sections" || queryKey[1] !== "library") return true;
  const queryLibraryId = queryKey[2];
  return queryLibraryId == null || queryLibraryId === libraryId;
}
