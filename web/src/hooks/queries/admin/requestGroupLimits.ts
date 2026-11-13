/**
 * Several access groups' request terms at once, for moving members between
 * groups. Kept apart from ./requests, which is at its size limit.
 */
import { useQueries } from "@tanstack/react-query";
import { captureProfileRequestContext } from "@/api/client";
import { adminAuthorityScope, type AdminAuthority } from "@/api/v2/adminAuthority";
import { getAdminRequestGroupLimitV2, type RequestGroupLimit } from "@/api/v2/adminRequests";
import { adminKeys } from "../keys";
import { REQUESTS_STALE_TIME } from "../useRequests";

/**
 * Several access groups' request approval and limit, sharing
 * useRequestGroupLimit's cache (./requests). Loading until every one has loaded; failed
 * when any could not be read.
 */
export function useRequestGroupLimits(groupIds: number[], authority?: AdminAuthority) {
  const context = authority ?? captureProfileRequestContext();
  const results = useQueries({
    queries: groupIds.map((groupId) => ({
      queryKey: adminKeys.requestGroupLimit(groupId, adminAuthorityScope(context)),
      queryFn: () => getAdminRequestGroupLimitV2(groupId, context ?? undefined),
      enabled: groupId > 0,
      staleTime: REQUESTS_STALE_TIME,
      retry: false,
    })),
  });
  const limits = new Map<number, RequestGroupLimit>();
  results.forEach((result, index) => {
    if (result.data) limits.set(groupIds[index]!, result.data);
  });
  return {
    limits,
    isLoading: results.some((result) => result.isPending && result.fetchStatus !== "idle"),
    isError: results.some((result) => result.isError),
  };
}
