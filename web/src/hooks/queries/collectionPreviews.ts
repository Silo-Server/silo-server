import { useQuery } from "@tanstack/react-query";

import { v2 } from "@/api/v2/request";
import { previewToV2, previewFromV2 } from "@/api/personalCollections";
import type { CollectionPreviewRequest, QueryDefinition, QueryDefinitionInput } from "@/api/types";
import { normalizeQueryDefinition } from "@/api/types";

import { collectionKeys } from "./keys";

export function buildCollectionPreviewRequest(
  queryDefinition?: QueryDefinition | QueryDefinitionInput | null,
  limit = 12,
): CollectionPreviewRequest {
  return {
    query_definition: normalizeQueryDefinition(queryDefinition),
    limit,
  };
}

export function previewFingerprint(
  scope: "user" | "admin",
  request: CollectionPreviewRequest,
): string {
  return JSON.stringify({
    scope,
    limit: request.limit ?? 12,
    query_definition: normalizeQueryDefinition(request.query_definition),
  });
}

function useCollectionPreview(scope: "user" | "admin", request?: CollectionPreviewRequest | null) {
  const normalized = request
    ? buildCollectionPreviewRequest(request.query_definition, request.limit)
    : null;

  return useQuery({
    queryKey: normalized
      ? collectionKeys.preview(scope, previewFingerprint(scope, normalized))
      : collectionKeys.preview(scope, "disabled"),
    queryFn: () =>
      scope === "user"
        ? v2("POST /api/v2/collections/preview", { body: previewToV2(normalized!) }).then(
            previewFromV2,
          )
        : v2("POST /api/v2/admin/collections/preview", {
            body: { ...normalized!, limit: normalized!.limit ?? 12 },
          }),
    enabled: normalized !== null,
    staleTime: 30_000,
  });
}

export function useAdminCollectionPreview(request?: CollectionPreviewRequest | null) {
  return useCollectionPreview("admin", request);
}

export function useUserCollectionPreview(request?: CollectionPreviewRequest | null) {
  return useCollectionPreview("user", request);
}
