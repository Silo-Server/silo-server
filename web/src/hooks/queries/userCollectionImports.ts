import { useQuery } from "@tanstack/react-query";

import { v2, type V2Result } from "@/api/v2/request";
import { discoveryFromV2 } from "@/api/personalCollections";
import { TEMPLATE_STALE_TIME, type CollectionTemplateCatalog } from "@/lib/collectionTemplates";
import { PERSONAL_SCOPE, type ScopeKind } from "@/lib/collections/scope";
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

export type MDBListAccountList =
  V2Result<"GET /api/v2/admin/collections/mdblist/lists">["items"][number];

/**
 * The lists of the MDBList account whose key a scope's collections read with,
 * private ones included: for server collections, the account that owns the
 * server's MDBList API key; for personal ones, the acting profile's own
 * MDBList connection.
 */
export function useMDBListAccountLists(scopeKind: ScopeKind, enabled = true) {
  return useQuery({
    queryKey: collectionKeys.mdblistAccountLists(scopeKind),
    queryFn: () =>
      (scopeKind === "server"
        ? v2("GET /api/v2/admin/collections/mdblist/lists")
        : v2("GET /api/v2/collections/import/mdblist/mine")
      ).then((body) => body.items),
    enabled,
    staleTime: 60_000,
  });
}

export function useSyncUserCollection() {
  return useScopeSync(PERSONAL_SCOPE);
}
