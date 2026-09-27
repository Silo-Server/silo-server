import {
  nextAutoscanSourceObservation,
  observedAutoscanSource,
} from "./admin/autoscanSourceObservation";
import { readAdminAutoscanEvents, type AutoscanEventQuery } from "@/api/v2/adminAutoscanEvents";
import { readAdminAutoscanScans, type AutoscanScanQuery } from "@/api/v2/adminAutoscanScans";
import { v2 } from "@/api/v2/request";
import { readAdminAutoscanRewrites } from "@/api/v2/adminAutoscanRewrites";
import { readAdminAutoscanAvailableSources } from "@/api/v2/adminAutoscanAvailableSources";
import { readAdminAutoscanConnections } from "@/api/v2/adminAutoscanConnections";
import {
  readAdminAutoscanSettings,
  readAdminAutoscanStatus,
} from "@/api/v2/adminAutoscanInspection";
import { readAdminAutoscanSources } from "@/api/v2/adminAutoscanSources";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  AutoscanConnection,
  AutoscanConnectionInput,
  AutoscanConnectionTestInput,
  AutoscanConnectionTestResult,
  AutoscanSettings,
  AutoscanSource,
  AutoscanSourceCreateInput,
  AutoscanSourceInput,
} from "@/api/types";
import { adminKeys } from "./keys";

const AUTOSCAN_STALE_TIME = 30_000;
const AUTOSCAN_ACTIVITY_REFRESH_MS = 15_000;

// --- Settings ---

export function useAutoscanSettings() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanSettings(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    enabled: profileContext !== null,
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanSettings(profileContext);
    },
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

type AutoscanSettingsWriteIntent = {
  body: AutoscanSettings;
  profileContext: ProfileRequestContextSnapshot;
};
function captureAutoscanSettingsWrite(body: AutoscanSettings): AutoscanSettingsWriteIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { body: { ...body }, profileContext };
}
export function useUpdateAutoscanSettings() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      body,
      profileContext,
    }: AutoscanSettingsWriteIntent): Promise<AutoscanSettings> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("PUT /api/v2/admin/autoscan/settings", {
        body,
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      if (result.reschedule_state === "failed")
        toast.warning(
          "Settings saved, but poll-task rescheduling failed. Runtime may retain its previous schedule until restart.",
        );
      else if (result.reschedule_state === "not_configured")
        toast.warning("Settings saved; no poll-task rescheduler is configured on this server.");
      return result.settings;
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Autoscan settings saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSettings() });
    },
    onError: (_error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        "Settings persistence could not be confirmed. Reload settings before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (
      body: AutoscanSettings,
      options?: {
        onSuccess?: (result: AutoscanSettings) => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanSettingsWriteIntent;
      try {
        intent = captureAutoscanSettingsWrite(body);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: (result) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(result);
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (body: AutoscanSettings) =>
      mutation.mutateAsync(captureAutoscanSettingsWrite(body)),
  };
}

// --- Connections ---

export function useAutoscanConnections() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanConnections(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
    ],
    enabled: profileContext !== null,
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanConnections(profileContext);
    },
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

type AutoscanConnectionCreationIntent = {
  body: AutoscanConnectionInput;
  profileContext: ProfileRequestContextSnapshot;
};
function captureConnectionCreation(
  body: AutoscanConnectionInput,
): AutoscanConnectionCreationIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { body: { ...body }, profileContext };
}
export function useCreateAutoscanConnection() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      body,
      profileContext,
    }: AutoscanConnectionCreationIntent): Promise<AutoscanConnection> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/autoscan/connections", {
        body: { ...body, request_integration_id: body.request_integration_id ?? undefined },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Autoscan connection created");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
    },
    onError: (_error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        "Connection creation could not be confirmed. Refresh connections before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (
      body: AutoscanConnectionInput,
      options?: {
        onSuccess?: (result: AutoscanConnection) => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanConnectionCreationIntent;
      try {
        intent = captureConnectionCreation(body);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: (result) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(result);
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (body: AutoscanConnectionInput) =>
      mutation.mutateAsync(captureConnectionCreation(body)),
  };
}

type AutoscanConnectionUpdateIntent = AutoscanConnectionCreationIntent & { id: string };
function captureConnectionUpdate(input: {
  id: string;
  body: AutoscanConnectionInput;
}): AutoscanConnectionUpdateIntent {
  return { ...captureConnectionCreation(input.body), id: input.id };
}
export function useUpdateAutoscanConnection() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      id,
      body,
      profileContext,
    }: AutoscanConnectionUpdateIntent): Promise<AutoscanConnection> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("PUT /api/v2/admin/autoscan/connections/{id}", {
        path: { id },
        body: { ...body, request_integration_id: body.request_integration_id ?? undefined },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Autoscan connection updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
    },
    onError: (_error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        "Connection update could not be confirmed. Refresh connections before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (
      input: { id: string; body: AutoscanConnectionInput },
      options?: {
        onSuccess?: (result: AutoscanConnection) => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanConnectionUpdateIntent;
      try {
        intent = captureConnectionUpdate(input);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: (result) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(result);
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (input: { id: string; body: AutoscanConnectionInput }) =>
      mutation.mutateAsync(captureConnectionUpdate(input)),
  };
}

type AutoscanConnectionDeleteIntent = { id: string; profileContext: ProfileRequestContextSnapshot };
function captureConnectionDeletion(id: string): AutoscanConnectionDeleteIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { id, profileContext };
}
export function useDeleteAutoscanConnection() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({ id, profileContext }: AutoscanConnectionDeleteIntent): Promise<void> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/autoscan/connections/{id}", {
        path: { id },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Autoscan connection deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanConnections() });
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (_error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        "Connection deletion could not be confirmed. Refresh connections and check source bindings before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (
      id: string,
      options?: {
        onSuccess?: () => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanConnectionDeleteIntent;
      try {
        intent = captureConnectionDeletion(id);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: () => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.();
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (id: string) => mutation.mutateAsync(captureConnectionDeletion(id)),
  };
}

// --- Sources ---

export function useAutoscanSources() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanSources(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    enabled: profileContext !== null,
    queryFn: async () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      const observation = nextAutoscanSourceObservation();
      const sources = await readAdminAutoscanSources(profileContext);
      return sources.map((source) => observedAutoscanSource(source, observation));
    },
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

export function useAvailableScanSources() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanScanSourcePlugins(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    enabled: profileContext !== null,
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanAvailableSources(profileContext);
    },
    staleTime: AUTOSCAN_STALE_TIME,
  });
}

type SourceWriteIntent = {
  body: AutoscanSourceCreateInput | AutoscanSourceInput;
  id?: string;
  profileContext: ProfileRequestContextSnapshot;
};
function captureSourceWrite(
  body: SourceWriteIntent["body"],
  profileContext: ProfileRequestContextSnapshot | null,
  id?: string,
): SourceWriteIntent {
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  return { body: JSON.parse(JSON.stringify(body)), id, profileContext };
}
type SourceWriteCallbacks = {
  onSuccess?: (source: AutoscanSource) => void;
  onError?: (error: Error) => void;
};
function useAutoscanSourceWrite(create: boolean) {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async (intent: SourceWriteIntent): Promise<AutoscanSource> => {
      const { profileContext, body, id } = intent;
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const fields = {
        ...body,
        connection_id: body.connection_id ?? undefined,
        poll_interval_seconds: body.poll_interval_seconds ?? undefined,
        path_rewrites: body.path_rewrites ?? [],
      };
      const result = create
        ? await v2("POST /api/v2/admin/autoscan/sources", {
            body: {
              ...fields,
              plugin_id: (body as AutoscanSourceCreateInput).plugin_id,
              capability_id: (body as AutoscanSourceCreateInput).capability_id,
            },
            profileContext,
            retryAuthentication: false,
          })
        : await v2("PUT /api/v2/admin/autoscan/sources/{id}", {
            path: { id: id! },
            body: fields,
            profileContext,
            retryAuthentication: false,
          });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return {
        ...result,
        poll_interval_seconds: result.poll_interval_seconds ?? null,
        last_run_at: result.last_run_at ?? null,
        last_error: result.last_error ?? null,
      };
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
      toast.success(create ? "Autoscan source created" : "Autoscan source saved");
    },
    onError: (_error, intent) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext))
        toast.error(
          "Source write could not be confirmed. Refresh sources before another explicit submission.",
        );
    },
  });
}
function sourceWriteCallbacks(
  intent: SourceWriteIntent,
  options?: SourceWriteCallbacks,
): SourceWriteCallbacks {
  return {
    onSuccess: (result) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(result);
    },
    onError: (error) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
    },
  };
}
export function useCreateAutoscanSource(profileContext = captureProfileRequestContext()) {
  const mutation = useAutoscanSourceWrite(true);
  return {
    ...mutation,
    mutate: (body: AutoscanSourceCreateInput, options?: SourceWriteCallbacks) => {
      let intent: SourceWriteIntent;
      try {
        intent = captureSourceWrite(body, profileContext);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, sourceWriteCallbacks(intent, options));
    },
    mutateAsync: (body: AutoscanSourceCreateInput) =>
      mutation.mutateAsync(captureSourceWrite(body, profileContext)),
  };
}
export function useUpdateAutoscanSource(profileContext = captureProfileRequestContext()) {
  const mutation = useAutoscanSourceWrite(false);
  return {
    ...mutation,
    mutate: (
      { id, body }: { id: string; body: AutoscanSourceInput },
      options?: SourceWriteCallbacks,
    ) => {
      let intent: SourceWriteIntent;
      try {
        intent = captureSourceWrite(body, profileContext, id);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, sourceWriteCallbacks(intent, options));
    },
    mutateAsync: ({ id, body }: { id: string; body: AutoscanSourceInput }) =>
      mutation.mutateAsync(captureSourceWrite(body, profileContext, id)),
  };
}

export type AutoscanSourceDeleteIntent = {
  id: string;
  profileContext: ProfileRequestContextSnapshot;
};
export function captureSourceDeletion(
  id: string,
  profileContext = captureProfileRequestContext(),
): AutoscanSourceDeleteIntent {
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  return { id, profileContext };
}
export function useDeleteAutoscanSource() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({ id, profileContext }: AutoscanSourceDeleteIntent): Promise<void> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/autoscan/sources/{id}", {
        path: { id },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Autoscan source deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
    },
    onError: (_error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        "Source deletion could not be confirmed. Refresh sources before submitting again; running work may continue.",
      );
    },
  });
  return {
    ...mutation,
    mutateCaptured: (intent: AutoscanSourceDeleteIntent) => mutation.mutate(intent),
    mutate: (
      id: string,
      options?: {
        onSuccess?: () => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanSourceDeleteIntent;
      try {
        intent = captureSourceDeletion(id);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: () => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.();
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (id: string) => mutation.mutateAsync(captureSourceDeletion(id)),
  };
}

// --- Webhook endpoints ---

export type AutoscanWebhookIntent = { id: string; profileContext: ProfileRequestContextSnapshot };
export function captureAutoscanWebhookIntent(
  id: string,
  profileContext = captureProfileRequestContext(),
): AutoscanWebhookIntent {
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  return { id, profileContext };
}
type WebhookCallbacks = {
  onSuccess?: (source: AutoscanSource | null) => void;
  onError?: (error: Error) => void;
};
function useAutoscanWebhookLifecycle(
  action: "create" | "rotate" | "delete",
  profileContext: ProfileRequestContextSnapshot | null,
) {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (intent: AutoscanWebhookIntent): Promise<AutoscanSource | null> => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      const options = {
        path: { id: intent.id },
        profileContext: intent.profileContext,
        retryAuthentication: false,
      };
      let source: AutoscanSource | null = null;
      if (action === "delete")
        await v2("DELETE /api/v2/admin/autoscan/sources/{id}/webhook", options);
      else {
        const result =
          action === "create"
            ? await v2("POST /api/v2/admin/autoscan/sources/{id}/webhook", options)
            : await v2("POST /api/v2/admin/autoscan/sources/{id}/webhook/rotate", options);
        source = {
          ...result,
          poll_interval_seconds: result.poll_interval_seconds ?? null,
          last_run_at: result.last_run_at ?? null,
          last_error: result.last_error ?? null,
        };
      }
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      return source ? observedAutoscanSource(source, nextAutoscanSourceObservation()) : null;
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
      toast.success(
        action === "create"
          ? "Webhook endpoint created or already configured"
          : action === "rotate"
            ? "Webhook endpoint rotated. Refresh and copy the current URL to your provider."
            : "Webhook endpoint removed",
      );
    },
    onError: (_error, intent) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext))
        toast.error(
          "Webhook change could not be confirmed. Refresh source state before another explicit submission.",
        );
    },
  });
  const submit = (intent: AutoscanWebhookIntent, options?: WebhookCallbacks) =>
    mutation.mutate(intent, {
      onSuccess: (source) => {
        if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(source);
      },
      onError: (error) => {
        if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
      },
    });
  return {
    ...mutation,
    mutateCaptured: submit,
    mutate: (id: string, options?: WebhookCallbacks) => {
      let intent: AutoscanWebhookIntent;
      try {
        intent = captureAutoscanWebhookIntent(id, profileContext);
      } catch {
        return;
      }
      submit(intent, options);
    },
    mutateAsync: (id: string) =>
      mutation.mutateAsync(captureAutoscanWebhookIntent(id, profileContext)),
  };
}
export function useCreateAutoscanWebhook(profileContext = captureProfileRequestContext()) {
  return useAutoscanWebhookLifecycle("create", profileContext);
}
export function useRotateAutoscanWebhook(profileContext = captureProfileRequestContext()) {
  return useAutoscanWebhookLifecycle("rotate", profileContext);
}
export function useDeleteAutoscanWebhook(profileContext = captureProfileRequestContext()) {
  return useAutoscanWebhookLifecycle("delete", profileContext);
}

/**
 * Test an arr connection. Accepts either an existing connection id, or raw
 * credentials (base_url + api_key_ref) / a request integration id for an
 * unsaved dialog. Returns the result so the caller can render it inline;
 * errors are surfaced via the returned result, not a toast (advisory only).
 */
type AutoscanConnectionTestIntent = {
  body: AutoscanConnectionTestInput;
  profileContext: ProfileRequestContextSnapshot;
};
function captureConnectionTest(body: AutoscanConnectionTestInput): AutoscanConnectionTestIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { body: { ...body }, profileContext };
}
export function useTestAutoscanConnection() {
  const mutation = useMutation({
    mutationFn: async ({
      body,
      profileContext,
    }: AutoscanConnectionTestIntent): Promise<AutoscanConnectionTestResult> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/autoscan/connections/test", {
        body: {
          ...body,
          connection_id: body.connection_id ?? undefined,
          request_integration_id: body.request_integration_id ?? undefined,
        },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    retry: false,
  });
  return {
    ...mutation,
    mutate: (
      body: AutoscanConnectionTestInput,
      options?: {
        onSuccess?: (result: AutoscanConnectionTestResult) => void;
        onError?: (error: Error) => void;
      },
    ) => {
      let intent: AutoscanConnectionTestIntent;
      try {
        intent = captureConnectionTest(body);
      } catch {
        options?.onError?.(new StaleApiRequestContextError());
        return;
      }
      mutation.mutate(intent, {
        onSuccess: (result) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onSuccess?.(result);
        },
        onError: (error) => {
          if (isCapturedProfileAuthorityActive(intent.profileContext)) options?.onError?.(error);
        },
      });
    },
    mutateAsync: (body: AutoscanConnectionTestInput) =>
      mutation.mutateAsync(captureConnectionTest(body)),
  };
}

/** Explicit provider-read gesture; captured before offline queuing and never replayed automatically. */
export function useAutoscanRewriteSuggestions() {
  return useMutation({
    mutationFn: readAdminAutoscanRewrites,
    retry: false,
    onError: (err, intent) => {
      if (isCapturedProfileAuthorityActive(intent.profileContext))
        toast.error(err instanceof Error ? err.message : "Could not read rewrite suggestions");
    },
  });
}

// --- Status ---

export function useAutoscanStatus() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanStatus(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
    ],
    enabled: profileContext !== null,
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanStatus(profileContext);
    },
    staleTime: AUTOSCAN_STALE_TIME,
    refetchInterval: AUTOSCAN_ACTIVITY_REFRESH_MS,
  });
}

/** A single page of history rows plus the total matching count for pagination. */
export interface AutoscanPage<T> {
  rows: T[];
  total: number;
}

export function useAutoscanEvents(params: AutoscanEventQuery = {}) {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanEvents(params),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanEvents(profileContext, params);
    },
    staleTime: AUTOSCAN_ACTIVITY_REFRESH_MS,
    refetchInterval: AUTOSCAN_ACTIVITY_REFRESH_MS,
    enabled: profileContext !== null && (params.enabled ?? true),
  });
}

export function useAutoscanScans(params: AutoscanScanQuery = {}) {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.autoscanScans(params),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: () => {
      if (!profileContext) throw new StaleApiRequestContextError();
      return readAdminAutoscanScans(profileContext, params);
    },
    staleTime: AUTOSCAN_ACTIVITY_REFRESH_MS,
    refetchInterval: AUTOSCAN_ACTIVITY_REFRESH_MS,
    enabled: profileContext !== null && (params.enabled ?? true),
  });
}

// --- Trigger ---

export function useTriggerAutoscan() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async (authority: ProfileRequestContextSnapshot) => {
      if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/autoscan/trigger", {
        profileContext: authority,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
      return result;
    },
    onSuccess: (_task, authority) => {
      if (!isCapturedProfileAuthorityActive(authority)) return;
      toast.success(
        "Autoscan poll started on this server process. Check activity for source outcomes.",
      );
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanStatus() });
      queryClient.invalidateQueries({ queryKey: ["admin", "autoscan", "events"] });
    },
    onError: (_error, authority) => {
      if (isCapturedProfileAuthorityActive(authority))
        toast.error(
          "Autoscan start could not be confirmed. Check task and activity state before running again.",
        );
    },
  });
  return {
    ...mutation,
    mutate: () => {
      const authority = captureProfileRequestContext();
      if (!authority || !isCapturedProfileAuthorityActive(authority)) return;
      mutation.mutate(authority);
    },
  };
}
