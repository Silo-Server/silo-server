/**
 * Loads the Catalog route chunk. App.tsx lazy-loads the route through this
 * factory, and the search entry points call prefetchCatalog as soon as the user
 * starts a search, so a first catalog visit does not wait on the chunk.
 */
export function importCatalog() {
  return import("@/pages/Catalog");
}

/** Starts the Catalog chunk download ahead of navigation. */
export function prefetchCatalog() {
  // Nothing to report here: the route imports the chunk again when it renders
  // and reports a failure through the route's error handling.
  importCatalog().catch(() => undefined);
}
