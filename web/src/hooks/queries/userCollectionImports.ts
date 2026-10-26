import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import { discoveryFromV2 } from "@/api/personalCollections";
import { TEMPLATE_STALE_TIME, type CollectionTemplateCatalog } from "@/lib/collectionTemplates";
import { PERSONAL_SCOPE } from "@/lib/collections/scope";
import { useScopeSync } from "./collectionScope";
import { collectionKeys } from "./keys";

export function useUserCollectionTemplates(enabled = true) {
  return useQuery({
    queryKey: collectionKeys.templates(),
    queryFn: () =>
      v2("GET /api/v2/collections/templates").then((value) => value as CollectionTemplateCatalog),
    enabled,
    staleTime: TEMPLATE_STALE_TIME,
  });
}

export function useMDBListSearch(query: string, enabled = true) {
  const trimmed = query.trim();
  return useQuery({
    queryKey: collectionKeys.mdblistSearch(trimmed),
    queryFn: () =>
      v2("GET /api/v2/collections/import/mdblist/search", { query: { q: trimmed } }).then(
        discoveryFromV2,
      ),
    enabled: enabled && trimmed.length > 0,
    staleTime: 60_000,
  });
}

export function useSyncUserCollection() {
  return useScopeSync(PERSONAL_SCOPE);
}
