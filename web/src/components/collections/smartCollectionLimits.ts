import type { QueryDefinition } from "@/api/types";

export const SMART_COLLECTION_DEFAULT_LIMIT = 100;
export const SMART_COLLECTION_MAX_LIMIT = 500;

export function withSmartCollectionLimit(query: QueryDefinition): QueryDefinition {
  const limit =
    typeof query.limit === "number" && Number.isFinite(query.limit) && query.limit > 0
      ? Math.min(Math.floor(query.limit), SMART_COLLECTION_MAX_LIMIT)
      : SMART_COLLECTION_DEFAULT_LIMIT;
  return { ...query, limit };
}
