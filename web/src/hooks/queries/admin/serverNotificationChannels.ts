import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  NotificationWebhookTestResult,
  ServerNotificationChannel,
  ServerNotificationChannelInput,
} from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

export function useServerNotificationChannels() {
  return useQuery({
    queryKey: adminKeys.serverNotificationChannels(),
    queryFn: () =>
      api<{ channels: ServerNotificationChannel[] }>("/admin/notifications/server-channels").then(
        (d) => d.channels ?? [],
      ),
  });
}

export function useCreateServerNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: ServerNotificationChannelInput) =>
      api<ServerNotificationChannel>("/admin/notifications/server-channels", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: adminKeys.serverNotificationChannels() });
    },
  });
}

export function useUpdateServerNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...input }: ServerNotificationChannelInput & { id: string }) =>
      api<ServerNotificationChannel>(`/admin/notifications/server-channels/${id}`, {
        method: "PUT",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: adminKeys.serverNotificationChannels() });
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to update channel");
    },
  });
}

export function useDeleteServerNotificationChannel() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api(`/admin/notifications/server-channels/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Channel deleted");
      void queryClient.invalidateQueries({ queryKey: adminKeys.serverNotificationChannels() });
    },
    onError: () => {
      toast.error("Failed to delete channel");
    },
  });
}

export function useTestServerNotificationChannel() {
  return useMutation({
    mutationFn: (id: string) =>
      api<NotificationWebhookTestResult>(`/admin/notifications/server-channels/${id}/test`, {
        method: "POST",
      }),
  });
}

export function useRotateServerNotificationChannelSecret() {
  return useMutation({
    mutationFn: (id: string) =>
      api<{ signing_secret: string }>(`/admin/notifications/server-channels/${id}/rotate-secret`, {
        method: "POST",
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to rotate signing secret");
    },
  });
}
