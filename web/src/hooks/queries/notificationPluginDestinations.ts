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
import { problemFieldDetail, v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { t } from "@/i18n";
import { notificationKeys } from "./keys";

export type NotificationPluginChannel =
  V2Result<"GET /api/v2/notifications/plugin-channels">["items"][number];
export type NotificationPluginSettingsField =
  NotificationPluginChannel["settings_form"]["fields"][number];
export type NotificationPluginDestination =
  V2Result<"GET /api/v2/notifications/plugin-destinations">["items"][number];

export type NotificationPluginDestinationWrite = {
  name?: string;
  settings?: Record<string, unknown>;
  clear_secrets?: string[];
  enabled?: boolean;
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
// It is the plugin's text, shown as sent. It is null for any other error.
export function pluginSettingsRejection(error: unknown): string | null {
  if (!(error instanceof V2ProblemError)) return null;
  const rejection = error.problem.errors?.find((e) => e.location === "body.settings");
  return rejection?.detail || null;
}

const settingsFieldLocation = "body.settings.";

// pluginSettingsFieldErrors maps a 422's field errors at
// body.settings.<field> to each field's message, translated from the
// field error code. It is null when the error names no setting.
export function pluginSettingsFieldErrors(error: unknown): Record<string, string> | null {
  if (!(error instanceof V2ProblemError)) return null;
  const out: Record<string, string> = {};
  for (const fieldError of error.problem.errors ?? []) {
    const key = fieldError.location.startsWith(settingsFieldLocation)
      ? fieldError.location.slice(settingsFieldLocation.length)
      : "";
    if (key && !Object.hasOwn(out, key)) out[key] = problemFieldDetail(fieldError);
  }
  return Object.keys(out).length > 0 ? out : null;
}

/** The server's error on the destination's name, such as one already in use. */
export function pluginDestinationNameError(error: unknown): string | null {
  if (!(error instanceof V2ProblemError)) return null;
  const nameError = error.problem.errors?.find((e) => e.location === "body.name");
  return nameError ? problemFieldDetail(nameError) : null;
}

export function useNotificationPluginChannels(enabled = true) {
  const context = captureProfileRequestContext();
  return useQuery({
    queryKey: [...notificationKeys.pluginChannels(), notificationScope(context)],
    queryFn: async () => {
      const result = await v2("GET /api/v2/notifications/plugin-channels", {
        profileContext: context ?? captureNotificationAuthority(),
      });
      return result.items;
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
      input: Omit<NotificationPluginDestinationWrite, "name" | "clear_secrets"> & {
        plugin_id: string;
        capability_id: string;
        name: string;
      },
    ) => {
      if (!context) throw new StaleApiRequestContextError();
      requireNotificationAuthority(context);
      if (inFlight.current)
        throw new Error(t("web.settings.notifications.destinations.createInProgress"));
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
        throw new Error(t("web.settings.notifications.destinations.reloadBeforeEdit"));
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
        throw new Error(t("web.settings.notifications.destinations.reloadBeforeDelete"));
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
      toast.success(t("web.settings.notifications.destinations.deleted"));
      invalidate(context);
    },
    onError: (error) =>
      toast.error(
        pluginDestinationErrorMessage(
          error,
          t("web.settings.notifications.destinations.deleteFailed"),
        ),
      ),
  });
}

export function useTestNotificationPluginDestination() {
  const context = captureProfileRequestContext();
  const inFlight = useRef(false);
  return useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      if (!context) throw new StaleApiRequestContextError();
      if (inFlight.current)
        throw new Error(t("web.settings.notifications.destinations.testInProgress"));
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
