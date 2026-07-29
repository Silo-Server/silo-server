import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { RateLimitConfig, RateLimitUpdateResponse } from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

export function useRateLimitConfig() {
  return useQuery({
    queryKey: adminKeys.rateLimitConfig(),
    queryFn: () => api<RateLimitConfig>("/admin/rate-limits/config"),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpdateRateLimitConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (config: RateLimitConfig) =>
      api<RateLimitUpdateResponse>("/admin/rate-limits/config", {
        method: "PUT",
        body: JSON.stringify(config),
      }),
    onSuccess: async (data) => {
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
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save rate limit settings");
    },
  });
}
