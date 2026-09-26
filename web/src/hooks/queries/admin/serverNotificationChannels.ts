import { v2 } from "@/api/v2/request";
import { requireNotificationAuthority } from "@/api/v2/notifications";
import { useRef } from "react";
import { testNotificationDestination } from "@/api/v2/notificationDestinationTests";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import type { ServerNotificationChannelInput } from "@/api/types";
import {
  listNotificationServerChannels,
  deleteNotificationServerChannel,
} from "@/api/v2/notificationDestinations";
import { captureNotificationAuthority, notificationScope } from "@/api/v2/notifications";
import { adminKeys } from "../keys";
import { toast } from "sonner";

export function useServerNotificationChannels() {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminKeys.serverNotificationChannels(), notificationScope(context)],
    queryFn: () => listNotificationServerChannels(context ?? captureNotificationAuthority()),
    enabled: context !== null,
    retry: false,
  });
}

export function useCreateServerNotificationChannel() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (input: ServerNotificationChannelInput) => {
      if (!context) throw new StaleApiRequestContextError();
      requireNotificationAuthority(context);
      if (inFlight.current) throw new Error("Destination creation is already in progress.");
      if (!input.name || !input.url) throw new Error("A name and webhook URL are required.");
      inFlight.current = true;
      try {
        const result = await v2("POST /api/v2/admin/notifications/server-channels", {
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
      void queryClient.invalidateQueries({ queryKey: adminKeys.serverNotificationChannels() });
    },
  });
}

export function useUpdateServerNotificationChannel() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  type Input = ServerNotificationChannelInput & { id: string };
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      input: Input;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      const { id, type: _type, ...body } = intent.input;
      requireNotificationAuthority(intent.authority);
      const result = await v2("PUT /api/v2/admin/notifications/server-channels/{id}", {
        path: { id },
        body,
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
        queryKey: [...adminKeys.serverNotificationChannels(), notificationScope(intent.authority)],
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
      toast.error(error instanceof Error ? error.message : "Failed to update channel");
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

export function useDeleteServerNotificationChannel() {
  const queryClient = useQueryClient();
  const context = captureProfileRequestContext();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      id: string;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      await deleteNotificationServerChannel(intent.id, intent.authority);
      return intent.authority;
    },
    onSuccess: (authority) => {
      requireNotificationAuthority(authority);
      toast.success("Channel deleted");
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.serverNotificationChannels(), notificationScope(authority)],
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
      toast.error("Failed to delete channel");
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

export function useTestServerNotificationChannel() {
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      if (!context) throw new StaleApiRequestContextError();
      if (inFlight.current) throw new Error("A test delivery is already in progress.");
      inFlight.current = true;
      try {
        return await testNotificationDestination("server-channel", id, context);
      } finally {
        inFlight.current = false;
      }
    },
  });
}

export function useRotateServerNotificationChannelSecret() {
  const context = captureProfileRequestContext();
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: {
      id: string;
      authority: ReturnType<typeof captureProfileRequestContext>;
    }) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      requireNotificationAuthority(intent.authority);
      const result = await v2(
        "POST /api/v2/admin/notifications/server-channels/{id}/rotate-secret",
        {
          path: { id: intent.id },
          profileContext: intent.authority,
          retryAuthentication: false,
        },
      );
      requireNotificationAuthority(intent.authority);
      return result;
    },
    onSuccess: (_result, intent) => {
      if (!intent.authority) throw new StaleApiRequestContextError();
      requireNotificationAuthority(intent.authority);
      void queryClient.invalidateQueries({
        queryKey: [...adminKeys.serverNotificationChannels(), notificationScope(intent.authority)],
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
