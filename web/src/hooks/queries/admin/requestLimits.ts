import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { t } from "@/i18n";
import { captureProfileRequestContext } from "@/api/client";
import { adminAuthorityScope, type AdminAuthority } from "@/api/v2/adminAuthority";
import {
  getAdminRequestUserLimitV2,
  putAdminRequestUserLimitV2,
  getAdminRequestGroupLimitV2,
  putAdminRequestGroupLimitV2,
  type RequestGroupLimit,
  type RequestGroupLimitBody,
} from "@/api/v2/adminRequests";
import type { RequestUserLimit } from "@/api/types";
import { adminKeys } from "../keys";
import { REQUESTS_STALE_TIME, invalidateRequestSurfaces } from "../useRequests";

export function useRequestUserLimit(userId?: number) {
  return useQuery({
    queryKey: adminKeys.requestUserLimit(userId ?? 0),
    queryFn: () => getAdminRequestUserLimitV2(userId!),
    enabled: Boolean(userId && userId > 0),
    staleTime: REQUESTS_STALE_TIME,
  });
}

export function useUpdateRequestUserLimit() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ userId, body }: { userId: number; body: RequestUserLimit }) =>
      putAdminRequestUserLimitV2(userId, body),
    onSuccess: (saved, variables) => {
      toast.success(t("web.admin.requests.toast.settingsSaved"));
      // The editor adopts the saved record; the cache has to hold it first,
      // or a clean editor would follow the query back to the replaced one.
      queryClient.setQueryData(adminKeys.requestUserLimit(variables.userId), saved);
      invalidateRequestSurfaces(queryClient);
    },
    onError: (err, variables) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.requests.toast.limitSaveFailed"),
      );
      // A refused save (412) means someone else saved; read their version so
      // an explicit reload starts from it.
      queryClient.invalidateQueries({ queryKey: adminKeys.requestUserLimit(variables.userId) });
    },
  });
}

/**
 * An access group's request approval and limit. An editor passes the
 * authority it read the group under, so the limit it saves carries a
 * validator from the same profile.
 */
export function useRequestGroupLimit(groupId?: number | null, authority?: AdminAuthority) {
  const context = authority ?? captureProfileRequestContext();
  return useQuery({
    queryKey: adminKeys.requestGroupLimit(groupId ?? 0, adminAuthorityScope(context)),
    queryFn: () => getAdminRequestGroupLimitV2(groupId!, context ?? undefined),
    enabled: Boolean(groupId && groupId > 0),
    staleTime: REQUESTS_STALE_TIME,
    retry: false,
  });
}

/**
 * Saves an access group's request approval and limit. Silent: the group
 * editor saves it together with the group and reports both.
 */
export function useUpdateRequestGroupLimit() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      limit,
      body,
      profileContext,
    }: {
      limit: Pick<RequestGroupLimit, "group_id" | "etag">;
      body: RequestGroupLimitBody;
      /** The authority the limit was read under; the active one when omitted. */
      profileContext?: AdminAuthority;
    }) => putAdminRequestGroupLimitV2(limit, body, profileContext),
    onSuccess: (saved, { profileContext }) => {
      queryClient.setQueryData(
        adminKeys.requestGroupLimit(saved.group_id, adminAuthorityScope(profileContext)),
        saved,
      );
      invalidateRequestSurfaces(queryClient);
    },
    onError: (_err, { limit, profileContext }) => {
      queryClient.invalidateQueries({
        queryKey: adminKeys.requestGroupLimit(limit.group_id, adminAuthorityScope(profileContext)),
      });
    },
  });
}
