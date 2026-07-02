import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { api } from "@/api/client";
import type { AccessGroup, AccessGroupInput } from "@/api/types";
import { adminKeys } from "../keys";

const ADMIN_STALE_TIME = 30_000;

export function useAccessGroups() {
  return useQuery({
    queryKey: adminKeys.accessGroups(),
    queryFn: () => api<AccessGroup[]>("/admin/access-groups").then((data) => data ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useCreateAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AccessGroupInput) =>
      api<AccessGroup>("/admin/access-groups", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Access group created");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create access group");
    },
  });
}

export function useUpdateAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: AccessGroupInput }) =>
      api<AccessGroup>(`/admin/access-groups/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      toast.success("Access group updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(variables.id) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update access group");
    },
  });
}

export function useDeleteAccessGroup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/access-groups/${id}`, { method: "DELETE" }),
    onSuccess: (_data, id) => {
      toast.success("Access group deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroups() });
      queryClient.invalidateQueries({ queryKey: adminKeys.accessGroup(id) });
      queryClient.invalidateQueries({ queryKey: adminKeys.users() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete access group");
    },
  });
}
