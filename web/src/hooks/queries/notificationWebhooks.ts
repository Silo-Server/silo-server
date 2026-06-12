import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
import type {
  NotificationCapability,
  NotificationWebhook,
  NotificationWebhookInput,
  NotificationWebhookTestResult,
  WebPushSubscriptionView,
} from "@/api/types";
import { notificationKeys } from "./keys";
import { toast } from "sonner";

export function useNotificationCapability() {
  return useQuery({
    queryKey: notificationKeys.capability(),
    queryFn: () => api<NotificationCapability>("/notifications/capability"),
    staleTime: 5 * 60_000,
  });
}

export function useNotificationWebhooks(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.webhooks(),
    queryFn: () =>
      api<{ webhooks: NotificationWebhook[] }>("/notifications/webhooks").then(
        (d) => d.webhooks ?? [],
      ),
    enabled,
  });
}

export function useCreateNotificationWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (input: NotificationWebhookInput) =>
      api<NotificationWebhook>("/notifications/webhooks", {
        method: "POST",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webhooks() });
    },
  });
}

export function useUpdateNotificationWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...input }: NotificationWebhookInput & { id: string }) =>
      api<NotificationWebhook>(`/notifications/webhooks/${id}`, {
        method: "PUT",
        body: JSON.stringify(input),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webhooks() });
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to update webhook");
    },
  });
}

export function useDeleteNotificationWebhook() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api(`/notifications/webhooks/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Webhook deleted");
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webhooks() });
    },
    onError: () => {
      toast.error("Failed to delete webhook");
    },
  });
}

export function useTestNotificationWebhook() {
  return useMutation({
    mutationFn: (id: string) =>
      api<NotificationWebhookTestResult>(`/notifications/webhooks/${id}/test`, {
        method: "POST",
      }),
  });
}

export function useRotateNotificationWebhookSecret() {
  return useMutation({
    mutationFn: (id: string) =>
      api<{ signing_secret: string }>(`/notifications/webhooks/${id}/rotate-secret`, {
        method: "POST",
      }),
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to rotate signing secret");
    },
  });
}

export function useWebPushSubscriptions(enabled = true) {
  return useQuery({
    queryKey: notificationKeys.webPushSubscriptions(),
    queryFn: () =>
      api<{ subscriptions: WebPushSubscriptionView[] }>(
        "/notifications/web-push/subscriptions",
      ).then((d) => d.subscriptions ?? []),
    enabled,
  });
}

export function useDeleteWebPushSubscription() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      api(`/notifications/web-push/subscriptions/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webPushSubscriptions() });
    },
    onError: () => {
      toast.error("Failed to remove push subscription");
    },
  });
}
