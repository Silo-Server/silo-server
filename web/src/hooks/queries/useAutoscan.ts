import {
  autoscanSourceObservation,
  nextAutoscanSourceObservation,
  observedAutoscanSource,
} from "./admin/autoscanSourceObservation";
import { readAdminAutoscanEvents, type AutoscanEventQuery } from "@/api/v2/adminAutoscanEvents";
import { readAdminAutoscanScans, type AutoscanScanQuery } from "@/api/v2/adminAutoscanScans";
import { V2ProblemError, v2 } from "@/api/v2/request";
import { readAdminAutoscanRewrites } from "@/api/v2/adminAutoscanRewrites";
import { readAdminAutoscanAvailableSources } from "@/api/v2/adminAutoscanAvailableSources";
import { readAdminAutoscanConnections } from "@/api/v2/adminAutoscanConnections";
import {
  readAdminAutoscanSettings,
  readAdminAutoscanStatus,
} from "@/api/v2/adminAutoscanInspection";
import { readAdminAutoscanSources } from "@/api/v2/adminAutoscanSources";
import {
  useIsFetching,
  useIsMutating,
  useMutation,
  useQuery,
  useQueryClient,
  type QueryClient,
} from "@tanstack/react-query";
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

/** The source list's cache key under one profile authority. */
function autoscanSourcesKey(profileContext: ProfileRequestContextSnapshot | null) {
  return [
    ...adminKeys.autoscanSources(),
    profileContext?.serverOrigin,
    profileContext?.authContextVersion,
    profileContext?.profileId,
    profileContext?.profileTokenGeneration,
  ];
}

/**
 * Put a write's readback into the cached source list, so every view (the list
 * row, a reopened edit dialog) shows it before the follow-up read lands. An
 * entry the cache already holds from a newer observation is kept.
 */
function storeAutoscanSourceReadback(
  queryClient: QueryClient,
  profileContext: ProfileRequestContextSnapshot,
  source: AutoscanSource,
) {
  queryClient.setQueryData<AutoscanSource[]>(autoscanSourcesKey(profileContext), (list) =>
    list?.map((cached) =>
      cached.id === source.id &&
      autoscanSourceObservation(source) > autoscanSourceObservation(cached)
        ? source
        : cached,
    ),
  );
}

/**
 * The source list. With `enabled: false` the hook only observes whatever the
 * cache holds and never starts a read itself.
 */
export function useAutoscanSources({ enabled = true }: { enabled?: boolean } = {}) {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: autoscanSourcesKey(profileContext),
    enabled: enabled && profileContext !== null,
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
const autoscanSourceWriteKey = ["admin", "autoscan", "source-write"] as const;
function useAutoscanSourceWrite(create: boolean) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: autoscanSourceWriteKey,
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
      return observedAutoscanSource(
        {
          ...result,
          poll_interval_seconds: result.poll_interval_seconds ?? null,
          last_run_at: result.last_run_at ?? null,
          last_error: result.last_error ?? null,
        },
        nextAutoscanSourceObservation(),
      );
    },
    onSuccess: (result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      // The next edit starts from the saved source even if it opens before
      // the follow-up read lands.
      if (!create) storeAutoscanSourceReadback(queryClient, intent.profileContext, result);
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
      toast.success(create ? "Autoscan source created" : "Autoscan source saved");
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      // A validation refusal is a definite answer: nothing was written. Show
      // the server's reason instead of the uncertain-outcome message.
      const rejection = sourceWriteRejection(error);
      toast.error(
        rejection ??
          "Source write could not be confirmed. Refresh sources before another explicit submission.",
      );
    },
  });
}
/**
 * The server's reason when it refused a source write as invalid. A refusal by
 * the source rules carries one fixed detail and no field errors; a schema
 * failure says "see errors", so the first field's detail is used instead.
 */
function sourceWriteRejection(error: unknown): string | null {
  if (!(error instanceof V2ProblemError) || error.status !== 422) return null;
  const detail = error.problem.detail;
  if (detail && !/see errors/i.test(detail)) return detail;
  return error.problem.errors?.find((e) => e.detail)?.detail || detail || null;
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

/**
 * True while a source's stored state may be about to change under a row: the
 * source list is being read, or a create/update for this source is in flight.
 * A row's quick action replaces the whole source from the row's snapshot, so
 * it must wait; otherwise it could overwrite a newer edit with older values.
 */
export function useAutoscanSourceBusy(sourceId: string): boolean {
  const reading = useIsFetching({ queryKey: adminKeys.autoscanSources() });
  const writing = useIsMutating({
    mutationKey: autoscanSourceWriteKey,
    predicate: (mutation) =>
      (mutation.state.variables as SourceWriteIntent | undefined)?.id === sourceId,
  });
  return reading > 0 || writing > 0;
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
  onSuccess?: (source: AutoscanSource) => void;
  onError?: (error: Error) => void;
};
/**
 * Per-source state shared by every view of a webhook URL (the list row and the
 * edit dialog). `pending` is set while a create/rotate is in flight;
 * `failedObservation` is the observation number reserved when one failed
 * without a confirmed outcome. While either applies, no view may offer the
 * cached URL — the secret may already be dead.
 */
type AutoscanWebhookUncertainty = { pending: boolean; failedObservation?: number };

export const autoscanWebhookUncertainKey = (sourceId: string) =>
  ["admin", "autoscan", "webhook-uncertain", sourceId] as const;

/**
 * Whether a view of this source's webhook URL must hide it: a change is in
 * flight, or one failed and no successful source read has started since. List
 * reads reserve their observation number before dispatch, so a read already in
 * flight when the change failed does not count. A failed read keeps the URL
 * hidden; the next successful one, from any trigger, shows the current URL.
 */
export function useAutoscanWebhookUncertain(sourceId: string): boolean {
  const { data: state } = useQuery<AutoscanWebhookUncertainty | null>({
    queryKey: autoscanWebhookUncertainKey(sourceId),
    queryFn: () => null,
    enabled: false,
    initialData: null,
    staleTime: Infinity,
  });
  const failedObservation = state?.failedObservation;
  // Only an unconfirmed failure needs a fresh read; otherwise just observe.
  const { data: sources } = useAutoscanSources({ enabled: failedObservation !== undefined });
  if (state?.pending) return true;
  if (failedObservation === undefined) return false;
  const current = sources?.find((source) => source.id === sourceId);
  return autoscanSourceObservation(current) <= failedObservation;
}

function useAutoscanWebhookLifecycle(
  action: "create" | "rotate",
  profileContext: ProfileRequestContextSnapshot | null,
) {
  const queryClient = useQueryClient();
  const setUncertainty = (id: string, state: AutoscanWebhookUncertainty | null) =>
    queryClient.setQueryData(autoscanWebhookUncertainKey(id), state);
  const mutation = useMutation({
    retry: false,
    onMutate: (intent: AutoscanWebhookIntent) => setUncertainty(intent.id, { pending: true }),
    mutationFn: async (intent: AutoscanWebhookIntent): Promise<AutoscanSource> => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      const options = {
        path: { id: intent.id },
        profileContext: intent.profileContext,
        retryAuthentication: false,
      };
      const result =
        action === "create"
          ? await v2("POST /api/v2/admin/autoscan/sources/{id}/webhook", options)
          : await v2("POST /api/v2/admin/autoscan/sources/{id}/webhook/rotate", options);
      const source = {
        ...result,
        poll_interval_seconds: result.poll_interval_seconds ?? null,
        last_run_at: result.last_run_at ?? null,
        last_error: result.last_error ?? null,
      };
      if (!isCapturedProfileAuthorityActive(intent.profileContext))
        throw new StaleApiRequestContextError();
      return observedAutoscanSource(source, nextAutoscanSourceObservation());
    },
    onSuccess: (result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) {
        setUncertainty(intent.id, null);
        return;
      }
      // Every view reads the URL from the list, so the new one goes there
      // before the uncertainty clears; otherwise a view that did not run the
      // change would offer the replaced (revoked) URL until the re-read lands.
      storeAutoscanSourceReadback(queryClient, intent.profileContext, result);
      setUncertainty(intent.id, null);
      queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
      toast.success(
        action === "create"
          ? "Webhook endpoint created or already configured"
          : "Webhook URL rotated. Copy the new URL into Sonarr or Radarr; the old one no longer works.",
      );
    },
    onError: (_error, intent) => {
      // After a profile switch every view re-reads its sources under the new
      // authority anyway; this request must not touch that cache.
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) {
        setUncertainty(intent.id, null);
        return;
      }
      // The change may have landed. Only a source read that starts after this
      // failure tells every view which URL is current again.
      setUncertainty(intent.id, {
        pending: false,
        failedObservation: nextAutoscanSourceObservation(),
      });
      void queryClient.invalidateQueries({ queryKey: adminKeys.autoscanSources() });
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
  const refreshPollState = () => {
    queryClient.invalidateQueries({ queryKey: adminKeys.autoscanStatus() });
    queryClient.invalidateQueries({ queryKey: ["admin", "autoscan", "events"] });
  };
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
      refreshPollState();
    },
    onError: (error, authority) => {
      if (!isCapturedProfileAuthorityActive(authority)) return;
      // The server refuses a start while this process is already polling. That
      // is a known state, not an uncertain outcome. The running poll may be a
      // scheduled one, which skips sources polled within their interval, so
      // the admin is told to press again once it finishes.
      if (isAutoscanAlreadyRunning(error)) {
        toast.info(
          "A poll is already running and may skip recently polled sources. Press Run now again when it finishes to poll every source.",
        );
        refreshPollState();
        return;
      }
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

/**
 * Whether a trigger was refused because the poll task is already running on
 * this process: a `conflict` problem is the trigger's only documented 409.
 */
function isAutoscanAlreadyRunning(error: unknown): boolean {
  return (
    error instanceof V2ProblemError && error.status === 409 && error.problemType === "conflict"
  );
}
