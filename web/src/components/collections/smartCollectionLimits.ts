import type { QueryDefinition } from "@/api/types";

/**
 * Mirrors catalog.DefaultSmartCollectionItemLimit. Smart collections are
 * uncapped by default; the server stores this sentinel in query_definition
 * when a collection sets no limit.
 */
const SMART_COLLECTION_NO_LIMIT_SENTINEL = 10_000_000;

/**
 * Clears a limit that means "no limit" (absent, not positive, or exactly the
 * server's sentinel) so the editor shows a blank field and a save omits it,
 * letting the server apply its default. Any other limit, including an explicit
 * one above the sentinel, passes through unchanged.
 */
export function normalizeSmartCollectionLimit(query: QueryDefinition): QueryDefinition {
  const limit = query.limit ?? 0;
  return limit > 0 && limit !== SMART_COLLECTION_NO_LIMIT_SENTINEL
    ? query
    : { ...query, limit: undefined };
}
