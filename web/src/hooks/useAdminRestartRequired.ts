import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { api } from "@/api/client";
import type { AdminServerStatus } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";

export function useAdminServerStatus() {
  return useQuery({
    queryKey: adminKeys.serverStatus(),
    queryFn: () => api<AdminServerStatus>("/admin/server/status"),
    staleTime: 5_000,
    refetchInterval: (query) => (query.state.data?.restart_requested ? 2_000 : 15_000),
  });
}

export function useAdminRestartRequired() {
  const query = useAdminServerStatus();
  return Boolean(query.data?.restart_required);
}

export function useRequestAdminServerRestart() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () => api("/admin/server/restart", { method: "POST" }),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: adminKeys.serverStatus() });
    },
  });
}
