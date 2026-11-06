import { useRef } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";

import { captureProfileRequestContext, StaleApiRequestContextError } from "@/api/client";
import { listNotificationPluginDestinations } from "@/api/v2/notificationDestinations";
import {
  captureNotificationAuthority,
  notificationScope,
  requireNotificationAuthority,
} from "@/api/v2/notifications";
import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import type { PluginConfigSchema } from "@/api/types";
import { notificationKeys } from "./keys";

export type NotificationPluginChannel = Omit<
  V2Result<"GET /api/v2/notifications/plugin-channels">["items"][number],
  "settings_form"
> & {
  // The v2 form schema is the admin plugin form the SchemaForm renders.
  settings_form: PluginConfigSchema;
};
export type NotificationPluginDestination =
  V2Result<"GET /api/v2/notifications/plugin-destinations">["items"][number];

export type NotificationPluginDestinationWrite = {
  name?: string;
  settings?: Record<string, unknown>;
  clear_secrets?: string[];
  enabled?: boolean;
  notify_favorites?: boolean;
  notify_watchlist?: boolean;
  notify_continue_watching?: boolean;
  notify_next_up?: boolean;
  notify_requests?: boolean;
};

/** The plugin's own reason for refusing settings, when the server passed one on. */
export function pluginDestinationErrorMessage(error: unknown, fallback: string): string {
  if (error instanceof V2ProblemError) {
    const detail = error.problem.errors?.[0]?.detail;
    return detail || error.message || fallback;
  }
  return error instanceof Error ? error.message : fallback;
}

// pluginSettingsRejection is the plugin's own reason for refusing a
// destination's settings, which the dialog shows under the settings fields.
// It is null for any other error.
export function pluginSettingsRejection(error: unknown): string | null {
  if (!(error instanceof V2ProblemError)) return null;
  const rejection = error.problem.errors?.find((e) => e.location === "body.settings");
  return rejection?.detail || null;
}

export function useNotificationPluginChannels(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.pluginChannels(), notificationScope(context)],
    queryFn: async () => {
      const result = await v2("GET /api/v2/notifications/plugin-channels", {
        profileContext: context ?? captureNotificationAuthority(),
      });
      return result.items as unknown as NotificationPluginChannel[];
    },
    enabled: enabled && context !== null,
    retry: false,
    staleTime: 60_000,
  });
}

export function useNotificationPluginDestinations(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.pluginDestinations(), notificationScope(context)],
    queryFn: () => listNotificationPluginDestinations(context ?? captureNotificationAuthority()),
    enabled: enabled && context !== null,
    retry: false,
  });
}

function useInvalidateDestinations() {
  const queryClient = useQueryClient();
  return (authority: ReturnType<typeof captureProfileRequestContext>) => {
    if (!authority) return;
    void queryClient.invalidateQueries({
      queryKey: [...notificationKeys.pluginDestinations(), notificationScope(authority)],
      exact: true,
    });
  };
}

export function useCreateNotificationPluginDestination() {
  const context = captureProfileRequestContext();
  const invalidate = useInvalidateDestinations();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (
      input: NotificationPluginDestinationWrite & { plugin_id: string; capability_id: string },
    ) => {
      if (!context) throw new StaleApiRequestContextError();
      requireNotificationAuthority(context);
      if (inFlight.current) throw new Error("Destination creation is already in progress.");
      inFlight.current = true;
      try {
        const result = await v2("POST /api/v2/notifications/plugin-destinations", {
          body: input,
          profileContext: context,
          retryAuthentication: false,
        });
        requireNotificationAuthority(context);
        return result;
      } finally {
        inFlight.current = false;
      }
    },
    onSuccess: () => invalidate(context),
  });
}

export function useUpdateNotificationPluginDestination() {
  const context = captureProfileRequestContext();
  const invalidate = useInvalidateDestinations();
  return useMutation({
    retry: false,
    mutationFn: async ({
      id,
      etag,
      ...body
    }: NotificationPluginDestinationWrite & { id: string; etag: string }) => {
      if (!context) throw new StaleApiRequestContextError();
      if (!etag || etag === "*")
        throw new Error("Reload the destination list before editing this destination.");
      requireNotificationAuthority(context);
      const result = await v2("PUT /api/v2/notifications/plugin-destinations/{id}", {
        path: { id },
        body,
        headers: { "If-Match": etag },
        profileContext: context,
        retryAuthentication: false,
      });
      requireNotificationAuthority(context);
      return result;
    },
    onSuccess: () => invalidate(context),
  });
}

export function useDeleteNotificationPluginDestination() {
  const context = captureProfileRequestContext();
  const invalidate = useInvalidateDestinations();
  return useMutation({
    retry: false,
    mutationFn: async ({ id, etag }: { id: string; etag: string }) => {
      if (!context) throw new StaleApiRequestContextError();
      if (!etag || etag === "*")
        throw new Error("Reload the destination list before deleting this destination.");
      requireNotificationAuthority(context);
      await v2("DELETE /api/v2/notifications/plugin-destinations/{id}", {
        path: { id },
        headers: { "If-Match": etag },
        profileContext: context,
        retryAuthentication: false,
      });
      requireNotificationAuthority(context);
    },
    onSuccess: () => {
      toast.success("Destination deleted");
      invalidate(context);
    },
    onError: (error) =>
      toast.error(pluginDestinationErrorMessage(error, "Failed to delete destination")),
  });
}

export function useTestNotificationPluginDestination() {
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      if (!context) throw new StaleApiRequestContextError();
      if (inFlight.current) throw new Error("A test message is already being sent.");
      inFlight.current = true;
      try {
        requireNotificationAuthority(context);
        const result = await v2("POST /api/v2/notifications/plugin-destinations/{id}/test", {
          path: { id },
          profileContext: context,
          retryAuthentication: false,
        });
        requireNotificationAuthority(context);
        return result;
      } finally {
        inFlight.current = false;
      }
    },
  });
}
