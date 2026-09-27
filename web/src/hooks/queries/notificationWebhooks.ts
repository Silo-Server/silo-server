import { v2 } from "@/api/v2/request";
import { requireNotificationAuthority } from "@/api/v2/notifications";
import { useRef } from "react";
import { testNotificationDestination } from "@/api/v2/notificationDestinationTests";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import {
  notificationCapabilities,
  notificationScope,
  captureNotificationAuthority,
} from "@/api/v2/notifications";
import type { NotificationWebhookInput } from "@/api/types";
import {
  listNotificationWebPushSubscriptions,
  deleteNotificationWebPushSubscription,
  deleteNotificationWebhook,
  rotateNotificationWebhookSecret,
  listNotificationWebhooks,
} from "@/api/v2/notificationDestinations";
import { notificationKeys } from "./keys";
import { toast } from "sonner";

export function useNotificationCapability() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.capability(), notificationScope(context)],
    queryFn: () => notificationCapabilities(context ?? captureNotificationAuthority()),
    enabled: context !== null,
    retry: false,
    staleTime: 5 * 60_000,
  });
}

export function useNotificationWebhooks(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.webhooks(), notificationScope(context)],
    queryFn: () => listNotificationWebhooks(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    retry: false,
  });
}

export function useCreateNotificationWebhook() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (input: NotificationWebhookInput) => {
      if (!context) throw new StaleApiRequestContextError();
      requireNotificationAuthority(context);
      if (inFlight.current) throw new Error("Destination creation is already in progress.");
      if (!input.name || !input.url) throw new Error("A name and webhook URL are required.");
      inFlight.current = true;
      try {
        const result = await v2("POST /api/v2/notifications/webhooks", {
          body: { ...input, name: input.name, url: input.url },
          profileContext: context,
          retryAuthentication: false,
        });
        requireNotificationAuthority(context);
        return result;
      } finally {
        inFlight.current = false;
      }
    },
    onSuccess: () => {
      if (!context) return;
      requireNotificationAuthority(context);
      void queryClient.invalidateQueries({ queryKey: notificationKeys.webhooks() });
    },
  });
}

export function useUpdateNotificationWebhook() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  type Input = NotificationWebhookInput & { id: string; etag: string };
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      input: Input;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      const { id, etag, type: _type, ...body } = intent.input;
      if (!etag || etag === "*")
        throw new Error("Reload the webhook list before editing this destination.");
      requireNotificationAuthority(intent.authority);
      const result = await v2("PUT /api/v2/notifications/webhooks/{id}", {
        path: { id },
        body,
        headers: { "If-Match": etag },
        profileContext: intent.authority,
        retryAuthentication: false,
      });
      requireNotificationAuthority(intent.authority);
      return result;
    },
    onSuccess: (_result, intent) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      requireNotificationAuthority(intent.authority);
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.webhooks(), notificationScope(intent.authority)],
        exact: true,
      });
    },
    onError: (error, intent) => {
      if (!intent.authority) return;
      try {
        requireNotificationAuthority(intent.authority);
      } catch {
        return;
      }
      toast.error(error instanceof Error ? error.message : "Failed to update webhook");
    },
  });
  return {
    ...mutation,
    mutate: (input: Input, options?: Parameters<typeof mutation.mutate>[1]) =>
      mutation.mutate({ input: { ...input }, authority: context }, options),
    mutateAsync: (input: Input, options?: Parameters<typeof mutation.mutateAsync>[1]) =>
      mutation.mutateAsync({ input: { ...input }, authority: context }, options),
  };
}

export function useDeleteNotificationWebhook() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      id: string;
      etag: string;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      if (!intent.etag || intent.etag === "*")
        throw new Error("Reload the webhook list before deleting this destination.");
      await deleteNotificationWebhook(intent, intent.authority);
      return intent.authority;
    },
    onSuccess: (authority) => {
      requireNotificationAuthority(authority);
      toast.success("Webhook deleted");
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.webhooks(), notificationScope(authority)],
        exact: true,
      });
    },
    onError: (_error, intent) => {
      const context = intent.authority;
      if (!context) return;
      try {
        requireNotificationAuthority(context);
      } catch {
        return;
      }
      toast.error("Failed to delete webhook");
    },
  });
  return {
    ...mutation,
    mutate: (
      intent: { id: string; etag: string },
      options?: Parameters<typeof mutation.mutate>[1],
    ) => mutation.mutate({ ...intent, authority: context }, options),
    mutateAsync: (
      intent: { id: string; etag: string },
      options?: Parameters<typeof mutation.mutateAsync>[1],
    ) => mutation.mutateAsync({ ...intent, authority: context }, options),
  };
}

export function useTestNotificationWebhook() {
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      if (!context) throw new StaleApiRequestContextError();
      if (inFlight.current) throw new Error("A test delivery is already in progress.");
      inFlight.current = true;
      try {
        return await testNotificationDestination("webhook", id, context);
      } finally {
        inFlight.current = false;
      }
    },
  });
}

export function useRotateNotificationWebhookSecret() {
  const context = captureProfileRequestContext();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      id: string;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      return rotateNotificationWebhookSecret(intent.id, intent.authority);
    },
    onSuccess: (_result, intent) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      requireNotificationAuthority(intent.authority);
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.webhooks(), notificationScope(intent.authority)],
        exact: true,
      });
    },
    onError: (error, intent) => {
      if (!intent.authority) return;
      try {
        requireNotificationAuthority(intent.authority);
      } catch {
        return;
      }
      toast.error(error instanceof Error ? error.message : "Failed to rotate signing secret");
    },
  });
  return {
    ...mutation,
    mutate: (id: string, options?: Parameters<typeof mutation.mutate>[1]) =>
      mutation.mutate({ id, authority: context }, options),
    mutateAsync: (id: string, options?: Parameters<typeof mutation.mutateAsync>[1]) =>
      mutation.mutateAsync({ id, authority: context }, options),
  };
}

export function useWebPushSubscriptions(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.webPushSubscriptions(), notificationScope(context)],
    queryFn: () => listNotificationWebPushSubscriptions(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    retry: false,
  });
}

export function useDeleteWebPushSubscription() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      if (!context) throw new StaleApiRequestContextError();
      await deleteNotificationWebPushSubscription(id, context);
      return context;
    },
    onSuccess: (authority) => {
      requireNotificationAuthority(authority);
      void queryClient.invalidateQueries({
        queryKey: [...notificationKeys.webPushSubscriptions(), notificationScope(authority)],
        exact: true,
      });
    },
    onError: () => {
      toast.error("Failed to remove push subscription");
    },
  });
}
