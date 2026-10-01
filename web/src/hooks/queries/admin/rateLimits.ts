import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { RateLimitConfig } from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

import { v2, V2ProblemError } from "@/api/v2/request";

export type RateLimitSnapshot = RateLimitConfig & {
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};
export type RateLimitIntent = {
  config: RateLimitConfig;
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};

const ADMIN_STALE_TIME = 30_000;

export function useRateLimitConfig() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.rateLimitConfig(),
      context?.serverOrigin,
      context?.authContextVersion,
      context?.profileId,
    ],
    enabled: context !== null,
    queryFn: async (): Promise<RateLimitSnapshot> => {
      if (!context || !isCapturedProfileAuthorityActive(context))
        throw new StaleApiRequestContextError();
      let etag = "";
      const [config, status] = await Promise.all([
        v2("GET /api/v2/admin/rate-limits/config", {
          profileContext: context,
          onResponse: (response) => {
            etag = response.headers.get("ETag") ?? "";
          },
        }),
        v2("GET /api/v2/admin/rate-limits/status", { profileContext: context }),
      ]);
      if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
      if (!etag || etag.startsWith("W/"))
        throw new Error("Reload rate limit settings before editing.");
      return { ...config, ...status, etag, profileContext: context };
    },
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateRateLimitConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (intent: RateLimitIntent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      if (!intent.etag || intent.etag === "*" || intent.etag.startsWith("W/"))
        throw new Error("Reload rate limit settings before editing.");
      return v2("PATCH /api/v2/admin/rate-limits/config", {
        body: intent.config,
        profileContext: intent.profileContext,
        headers: { "If-Match": intent.etag },
        retryAuthentication: false,
      });
    },
    retry: false,
    onSuccess: async (data, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      if (data.restart_required) {
        toast.success("Rate limit settings saved — restart the server to apply them");
      } else {
        toast.success("Rate limit settings saved");
      }
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: adminKeys.rateLimitConfig() }),
        queryClient.invalidateQueries({ queryKey: adminKeys.serverStatus() }),
      ]);
    },
    onError: (err, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        err instanceof V2ProblemError && err.status === 412
          ? "Rate limit settings changed. Reload and review them before saving again."
          : err instanceof Error
            ? err.message
            : "Failed to save rate limit settings",
      );
    },
  });
}
