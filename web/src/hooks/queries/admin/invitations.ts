import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { Invitation, CreateInvitationRequest, SendInvitationResponse } from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

const ADMIN_STALE_TIME = 30_000;

export function useAdminInvitations() {
  return useQuery({
    queryKey: adminKeys.invitations(),
    queryFn: () => api<Invitation[]>("/admin/invitations").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useCreateInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateInvitationRequest) =>
      api<SendInvitationResponse>("/admin/invitations", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.invitations() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to send invitation");
    },
  });
}

export function useResendInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api<SendInvitationResponse>(`/admin/invitations/${id}/resend`, { method: "POST" }),
    onSuccess: (data) => {
      if (data.email_sent) {
        toast.success("Invitation resent — the old link no longer works");
      }
      queryClient.invalidateQueries({ queryKey: adminKeys.invitations() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to resend invitation");
    },
  });
}

export function useRevokeInvitation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/invitations/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Invitation revoked");
      queryClient.invalidateQueries({ queryKey: adminKeys.invitations() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to revoke invitation");
    },
  });
}
