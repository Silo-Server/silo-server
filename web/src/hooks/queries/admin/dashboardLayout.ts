import { useCallback } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "@/api/v2/request";
import type { AdminDashboardLayoutDocument, AdminDashboardLayoutResponse } from "@/api/types";
import { adminKeys } from "../keys";

// A single toast id per concern: a burst of failed saves (offline, server
// down) collapses into one message instead of stacking one per attempt.
const SAVE_TOAST_ID = "admin-dashboard-layout-save";
const RESET_TOAST_ID = "admin-dashboard-layout-reset";

/**
 * Writes to the layout row run one at a time.
 *
 * Same-scope mutations queue and execute in the order they were started, which
 * is what keeps the last write the admin made the one that wins: without it two
 * saves can overlap and the older document can land last, and a reset can
 * land before an in-flight save that then
 * resurrects the arrangement it just discarded.
 */
const LAYOUT_MUTATION_SCOPE = { id: "admin-dashboard-layout" } as const;

/** Reads the canonical account layout under the current request authority. */
export function useAdminDashboardLayout() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.dashboardLayout(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: async (): Promise<AdminDashboardLayoutResponse & { etag: string }> => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      let etag: string | null = null;
      const result = await v2("GET /api/v2/admin/dashboard/layout", {
        profileContext,
        onResponse: (response) => {
          etag = response.headers.get("ETag");
        },
      });
      if (!etag) throw new Error("Server layout validator missing.");
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return { layout: result.layout, updated_at: result.updated_at, etag };
    },
    enabled: profileContext !== null,
    staleTime: Infinity,
    gcTime: Infinity,
  });
}

export type DashboardLayoutSaveIntent = {
  layout: AdminDashboardLayoutDocument;
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};
export function captureDashboardLayoutSave(
  layout: AdminDashboardLayoutDocument,
  etag: string,
  profileContext = captureProfileRequestContext(),
): DashboardLayoutSaveIntent {
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  if (!etag) throw new Error("Read the server layout version before saving.");
  return {
    etag,
    layout: JSON.parse(JSON.stringify(layout)) as AdminDashboardLayoutDocument,
    profileContext,
  };
}
export function useSaveAdminDashboardLayout() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    scope: LAYOUT_MUTATION_SCOPE,
    retry: false,
    mutationFn: async (intent: DashboardLayoutSaveIntent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      let etag: string | null = null;
      await v2("PUT /api/v2/admin/dashboard/layout", {
        headers: { "If-Match": intent.etag },
        onResponse: (response) => {
          etag = response.headers.get("ETag");
        },
        body: { layout: { ...intent.layout } },
        profileContext: intent.profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      if (!etag)
        throw new Error("Server layout write revision missing; reload before saving again.");
      return etag;
    },
    onSuccess: (_result, intent) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext))
        return queryClient.invalidateQueries({ queryKey: adminKeys.dashboardLayout() });
    },
    onError: (_error, intent) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext))
        toast.error(
          "Server layout save could not be confirmed. Local edits are retained; reload before another server submission.",
          { id: SAVE_TOAST_ID },
        );
    },
  });
  const rawMutate = mutation.mutate;
  const rawMutateAsync = mutation.mutateAsync;
  const mutateCaptured = useCallback(
    (
      intent: DashboardLayoutSaveIntent,
      options?: { onSuccess?: (etag: string) => void; onError?: () => void },
    ) => {
      // Promise completion survives observer unmount; per-call mutate callbacks
      // do not. Cleanup may still hold a newer copied edit behind this write.
      void rawMutateAsync(intent).then(
        (etag) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(etag);
          else options?.onError?.();
        },
        () => options?.onError?.(),
      );
    },
    [rawMutateAsync],
  );
  const mutate = useCallback(
    (layout: AdminDashboardLayoutDocument, etag: string) => {
      try {
        rawMutate(captureDashboardLayoutSave(layout, etag));
      } catch {
        toast.error("Select an administrator profile before saving the server layout.", {
          id: SAVE_TOAST_ID,
        });
      }
    },
    [rawMutate],
  );
  return {
    ...mutation,
    mutate,
    mutateCaptured,
    mutateAsync: (layout: AdminDashboardLayoutDocument, etag: string) =>
      mutation.mutateAsync(captureDashboardLayoutSave(layout, etag)),
  };
}

function captureLayoutReset(): ProfileRequestContextSnapshot {
  const authority = captureProfileRequestContext();
  if (!authority) throw new StaleApiRequestContextError();
  return authority;
}
export function useResetAdminDashboardLayout() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    scope: LAYOUT_MUTATION_SCOPE,
    retry: false,
    mutationFn: async (authority: ProfileRequestContextSnapshot) => {
      if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/dashboard/layout", {
        profileContext: authority,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
    },
    onSuccess: (_result, authority) => {
      if (isCapturedProfileAuthorityActive(authority))
        return queryClient.invalidateQueries({ queryKey: adminKeys.dashboardLayout() });
    },
    onError: (_error, authority) => {
      if (isCapturedProfileAuthorityActive(authority))
        toast.error("Server layout reset could not be confirmed. Reload before submitting again.", {
          id: RESET_TOAST_ID,
        });
    },
  });
  return {
    ...mutation,
    mutate: () => {
      try {
        mutation.mutate(captureLayoutReset());
      } catch {
        toast.error("Select an administrator profile before resetting the server layout.", {
          id: RESET_TOAST_ID,
        });
      }
    },
    mutateAsync: () => mutation.mutateAsync(captureLayoutReset()),
  };
}
