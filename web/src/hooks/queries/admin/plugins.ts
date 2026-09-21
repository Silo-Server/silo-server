import { v2, V2ProblemError, type V2Result } from "@/api/v2/request";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { toast } from "sonner";

import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  ConnectionCheckResponse,
  CreatePluginRepositoryRequest,
  InstallPluginRequest,
  PluginCatalogEntry,
  PluginCatalogSettings,
  PluginInstallation,
  PluginRepository,
  PluginTaskBindingUpdateResponse,
  SavePluginAuthBindingRequest,
  SavePluginConfigRequest,
  SavePluginTaskBindingRequest,
  UpdatePluginInstallationRequest,
  UpdatePluginCatalogSettingsRequest,
  UpdatePluginRepositoryRequest,
} from "@/api/types";
import { uploadAdminPlugin } from "@/api/v2/adminPluginUpload";
import type { ChunkedUploadProgress } from "@/api/v2/adminPluginUpload";
import { adminKeys } from "../keys";

const ADMIN_STALE_TIME = 30_000;
export const CHECK_PLUGIN_UPDATES_TASK_KEY = "check_plugin_updates";

function invalidatePluginQueries(queryClient: ReturnType<typeof useQueryClient>) {
  return Promise.all([
    queryClient.invalidateQueries({ queryKey: adminKeys.pluginRepositories() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.pluginCatalog() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.pluginInstallations() }),
    queryClient.invalidateQueries({ queryKey: adminKeys.pluginCatalogSettings() }),
  ]);
}

// useAdminPluginInstallations is a slim hook for callers (e.g. AdminSidebar)
// that only need the installations list. Shares its cache key with
// useAdminPlugins() so triggering a refetch in either keeps both in sync.
export function useAdminPluginInstallations() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [...adminKeys.pluginInstallations(), ...profileScopeKey(profileContext)],
    queryFn: () => fetchPluginInstallations(profileContext),
    staleTime: ADMIN_STALE_TIME,
    enabled: profileContext !== null,
  });
}

const PLUGIN_SOURCE_KINDS = new Set(["silo", "approved_community", "external"]);
const PLUGIN_PAGE_LIMIT = 100;
const PLUGIN_PAGE_CAP = 100;

function profileScopeKey(profileContext: ProfileRequestContextSnapshot | null) {
  return [
    profileContext?.serverOrigin,
    profileContext?.authContextVersion,
    profileContext?.profileId,
    profileContext?.profileTokenGeneration,
  ] as const;
}

function positiveIntegerOf(raw: string | undefined, what: string): number {
  const value = Number(raw);
  if (raw === undefined || !/^[1-9][0-9]*$/.test(raw) || !Number.isSafeInteger(value))
    throw new Error(`Invalid ${what} identifier in response.`);
  return value;
}

/**
 * Drains one cursor-paged v2 plugin collection under captured authority. The
 * server enumerates the full list on every page, so a repeated cursor, a
 * replaced authority mid-drain, or more than PLUGIN_PAGE_CAP pages fails the
 * read instead of merging inconsistent pages.
 */
async function drainPluginPages<Row>(
  profileContext: ProfileRequestContextSnapshot | null,
  fetchPage: (
    profileContext: ProfileRequestContextSnapshot,
    cursor: string | undefined,
  ) => Promise<{ items: Row[]; page?: { has_more: boolean; next_cursor?: string } }>,
  what: string,
): Promise<Row[]> {
  if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
    throw new StaleApiRequestContextError();
  const rows: Row[] = [];
  const cursors = new Set<string>();
  let cursor: string | undefined;
  for (let pageNumber = 0; pageNumber < PLUGIN_PAGE_CAP; pageNumber++) {
    const page = await fetchPage(profileContext, cursor);
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    rows.push(...page.items);
    if (!page.page) throw new Error(`Missing ${what} pagination metadata.`);
    if (!page.page.has_more) return rows;
    const next = page.page.next_cursor;
    if (!next || cursors.has(next)) throw new Error(`Invalid ${what} continuation.`);
    cursors.add(next);
    cursor = next;
  }
  throw new Error(`The ${what} list exceeds this client's page limit.`);
}

export async function fetchPluginCatalog(
  profileContext: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
): Promise<PluginCatalogEntry[]> {
  const rows = await drainPluginPages(
    profileContext,
    (context, cursor) =>
      v2("GET /api/v2/admin/plugins/catalog", {
        query: { limit: PLUGIN_PAGE_LIMIT, cursor },
        profileContext: context,
      }),
    "plugin catalog",
  );
  const seen = new Set<string>();
  return rows.map((row) => {
    const identity = `${row.plugin_id}@${row.version}`;
    if (seen.has(identity)) throw new Error("Duplicate plugin catalog entry in response.");
    seen.add(identity);
    if (!PLUGIN_SOURCE_KINDS.has(row.source_kind))
      throw new Error("Unrecognized plugin source kind.");
    return {
      ...row,
      repository_id: positiveIntegerOf(row.repository_id, "plugin repository"),
    } as PluginCatalogEntry;
  });
}

export async function fetchPluginInstallations(
  profileContext: ProfileRequestContextSnapshot | null = captureProfileRequestContext(),
): Promise<PluginInstallation[]> {
  const rows = await drainPluginPages(
    profileContext,
    (context, cursor) =>
      v2("GET /api/v2/admin/plugins/installations", {
        query: { limit: PLUGIN_PAGE_LIMIT, cursor },
        profileContext: context,
      }),
    "plugin installation",
  );
  const ids = new Set<number>();
  return rows.map((row) => {
    const installation = pluginInstallationOfV2(row);
    if (ids.has(installation.id)) throw new Error("Duplicate plugin installation in response.");
    ids.add(installation.id);
    return installation;
  });
}

type PluginInstallationRow = V2Result<"GET /api/v2/admin/plugins/installations">["items"][number];
/** Projects one v2 installation row onto the page's PluginInstallation shape. */
function pluginInstallationOfV2(row: PluginInstallationRow): PluginInstallation {
  const id = positiveIntegerOf(row.id, "plugin installation");
  if (!PLUGIN_SOURCE_KINDS.has(row.source_kind))
    throw new Error("Unrecognized plugin source kind.");
  return {
    ...row,
    id,
    repository_id:
      row.repository_id === undefined
        ? null
        : positiveIntegerOf(row.repository_id, "plugin repository"),
    available_version: row.available_version ?? null,
  } as PluginInstallation;
}

export function useAdminPluginRepositories() {
  const profileContext = captureProfileRequestContext();
  return useQuery({
    queryKey: [
      ...adminKeys.pluginRepositories(),
      profileContext?.serverOrigin,
      profileContext?.authContextVersion,
      profileContext?.profileId,
      profileContext?.profileTokenGeneration,
    ],
    queryFn: async (): Promise<PluginRepository[]> => {
      if (!profileContext || !isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const repositories: PluginRepository[] = [];
      const cursors = new Set<string>();
      const ids = new Set<string>();
      let cursor: string | undefined;
      for (let pageNumber = 0; pageNumber < 100; pageNumber++) {
        const page = await v2("GET /api/v2/admin/plugins/repositories", {
          query: { limit: 100, cursor },
          profileContext,
        });
        if (!isCapturedProfileAuthorityActive(profileContext))
          throw new StaleApiRequestContextError();
        for (const row of page.items) {
          const id = Number(row.id);
          if (!/^[1-9][0-9]*$/.test(row.id) || !Number.isSafeInteger(id) || ids.has(row.id))
            throw new Error("Invalid repository identifier in response.");
          if (
            row.source_kind !== "silo" &&
            row.source_kind !== "approved_community" &&
            row.source_kind !== "external"
          )
            throw new Error("Unrecognized repository source kind.");
          ids.add(row.id);
          repositories.push({ ...row, id, source_kind: row.source_kind });
        }
        if (!page.page) throw new Error("Missing repository pagination metadata.");
        if (!page.page.has_more) return repositories;
        const next = page.page.next_cursor;
        if (!next || cursors.has(next)) throw new Error("Invalid repository continuation.");
        cursors.add(next);
        cursor = next;
      }
      throw new Error("Repository list exceeds this client's page limit.");
    },
    staleTime: ADMIN_STALE_TIME,
    enabled: profileContext !== null,
  });
}

export function useAdminPlugins() {
  const repositoriesQuery = useAdminPluginRepositories();

  const profileContext = captureProfileRequestContext();
  const catalogQuery = useQuery({
    queryKey: [...adminKeys.pluginCatalog(), ...profileScopeKey(profileContext)],
    queryFn: () => fetchPluginCatalog(profileContext),
    staleTime: ADMIN_STALE_TIME,
    enabled: profileContext !== null,
  });

  const installationsQuery = useAdminPluginInstallations();

  const catalogSettingsQuery = useQuery({
    queryKey: adminKeys.pluginCatalogSettings(),
    queryFn: fetchPluginCatalogSettings,
    staleTime: ADMIN_STALE_TIME,
  });

  return {
    repositories: repositoriesQuery.data ?? [],
    repositoriesError: repositoriesQuery.error,
    catalog: catalogQuery.data ?? [],
    installations: installationsQuery.data ?? [],
    catalogSettings: catalogSettingsQuery.data,
    isLoading:
      repositoriesQuery.isLoading ||
      catalogQuery.isLoading ||
      installationsQuery.isLoading ||
      catalogSettingsQuery.isLoading,
    isFetching:
      repositoriesQuery.isFetching ||
      catalogQuery.isFetching ||
      installationsQuery.isFetching ||
      catalogSettingsQuery.isFetching,
  };
}

export type PluginCatalogSettingsView = PluginCatalogSettings & { etag: string };
type PluginCatalogSettingsUpdate = UpdatePluginCatalogSettingsRequest & { etag: string };
type PluginCatalogSettingsIntent = PluginCatalogSettingsUpdate & {
  profileContext: ProfileRequestContextSnapshot;
};

export async function fetchPluginCatalogSettings(): Promise<PluginCatalogSettingsView> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  let etag = "";
  const [settings, status] = await Promise.all([
    v2("GET /api/v2/admin/plugins/catalog-settings", {
      profileContext,
      onResponse: (response) => {
        etag = response.headers.get("ETag") ?? "";
      },
    }),
    v2("GET /api/v2/admin/plugins/catalog-status", { profileContext }),
  ]);
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  if (!etag || etag === "*" || etag.startsWith("W/"))
    throw new Error("Catalog revision unavailable. Reload before editing.");
  return {
    ...status,
    ...settings,
    etag,
    community_updates_paused:
      !settings.include_approved_community_plugins && status.installed_community_plugin_count > 0,
  };
}

export function useUpdatePluginCatalogSettings() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: ({ etag, profileContext, ...body }: PluginCatalogSettingsIntent) => {
      if (!etag || etag === "*" || etag.startsWith("W/"))
        throw new Error("Reload plugin catalog settings before editing.");
      return v2("PUT /api/v2/admin/plugins/catalog-settings", {
        body,
        profileContext,
        headers: { "If-Match": etag },
        retryAuthentication: false,
      });
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin catalog settings updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(
        error instanceof V2ProblemError && error.status === 412
          ? "Plugin catalog settings changed. Reload and review them before submitting another edit."
          : error instanceof Error
            ? error.message
            : "Failed to update plugin catalog settings",
      );
    },
  });
  return {
    ...mutation,
    mutate: (values: PluginCatalogSettingsUpdate) => {
      const profileContext = captureProfileRequestContext();
      if (!profileContext) {
        toast.error("Select an administrator profile before editing.");
        return;
      }
      mutation.mutate({ ...values, profileContext });
    },
  };
}

type PluginRepositoryCreationIntent = {
  body: CreatePluginRepositoryRequest;
  profileContext: ProfileRequestContextSnapshot;
};
function captureRepositoryCreation(
  body: CreatePluginRepositoryRequest,
): PluginRepositoryCreationIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { body: { ...body }, profileContext };
}
export function useCreatePluginRepository() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({ body, profileContext }: PluginRepositoryCreationIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/plugins/repositories", {
        body,
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
      toast.success("Repository added");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        error instanceof V2ProblemError && error.status === 422
          ? error.message
          : "Repository creation could not be confirmed. Refresh repositories before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (body: CreatePluginRepositoryRequest) => {
      try {
        mutation.mutate(captureRepositoryCreation(body));
      } catch {
        toast.error("Select an administrator profile before adding a repository.");
      }
    },
    mutateAsync: (body: CreatePluginRepositoryRequest) =>
      mutation.mutateAsync(captureRepositoryCreation(body)),
  };
}

type PluginRepositoryUpdateIntent = {
  id: number;
  body: UpdatePluginRepositoryRequest;
  profileContext: ProfileRequestContextSnapshot;
};
function captureRepositoryUpdate(input: {
  id: number;
  body: UpdatePluginRepositoryRequest;
}): PluginRepositoryUpdateIntent {
  if (!Number.isSafeInteger(input.id) || input.id <= 0) throw new Error("Invalid repository ID");
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { id: input.id, body: { ...input.body }, profileContext };
}
export function useUpdatePluginRepository() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({ id, body, profileContext }: PluginRepositoryUpdateIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("PUT /api/v2/admin/plugins/repositories/{id}", {
        path: { id: String(id) },
        body,
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
      toast.success("Repository updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        error instanceof V2ProblemError && error.status === 422
          ? error.message
          : "Repository update could not be confirmed. Refresh repositories before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (input: { id: number; body: UpdatePluginRepositoryRequest }) => {
      try {
        mutation.mutate(captureRepositoryUpdate(input));
      } catch {
        toast.error("Select an administrator profile before updating a repository.");
      }
    },
    mutateAsync: (input: { id: number; body: UpdatePluginRepositoryRequest }) =>
      mutation.mutateAsync(captureRepositoryUpdate(input)),
  };
}

type PluginRepositoryDeletionIntent = { id: number; profileContext: ProfileRequestContextSnapshot };
function captureRepositoryDeletion(id: number): PluginRepositoryDeletionIntent {
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error("Invalid repository ID");
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { id, profileContext };
}
export function useDeletePluginRepository() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({ id, profileContext }: PluginRepositoryDeletionIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/plugins/repositories/{id}", {
        path: { id: String(id) },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Repository removed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        error instanceof V2ProblemError && error.status === 422
          ? error.message
          : "Repository deletion could not be confirmed. Refresh repositories before submitting again.",
      );
    },
  });
  return {
    ...mutation,
    mutate: (id: number) => {
      try {
        mutation.mutate(captureRepositoryDeletion(id));
      } catch {
        toast.error("Select an administrator profile before deleting a repository.");
      }
    },
    mutateAsync: (id: number) => mutation.mutateAsync(captureRepositoryDeletion(id)),
  };
}

/**
 * Installation lifecycle intents capture their authority at click time, send
 * once (no automatic or authentication replay: create/apply/delete have no
 * replay identity), and refuse to run once the authority changed.
 */
type PluginInstallIntent = {
  body: InstallPluginRequest;
  profileContext: ProfileRequestContextSnapshot;
};
function captureInstall(body: InstallPluginRequest): PluginInstallIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { body: { ...body }, profileContext };
}
type PluginLifecycleIntent = { id: number; profileContext: ProfileRequestContextSnapshot };
function captureInstallation(id: number): PluginLifecycleIntent {
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error("Invalid installation ID");
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { id, profileContext };
}
function lifecycleFailure(error: unknown, fallback: string): string {
  return error instanceof V2ProblemError && (error.status === 422 || error.status === 409)
    ? error.message
    : fallback;
}

export function useInstallPlugin() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ body, profileContext }: PluginInstallIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const row = await v2("POST /api/v2/admin/plugins/installations", {
        body: {
          repository_id: body.repository_id === undefined ? undefined : String(body.repository_id),
          plugin_id: body.plugin_id,
          version: body.version,
          archive_url: body.archive_url,
        },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return pluginInstallationOfV2(row);
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin installed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        lifecycleFailure(
          error,
          "Plugin install could not be confirmed. Refresh installations before submitting again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (body: InstallPluginRequest) => {
      try {
        mutation.mutate(captureInstall(body));
      } catch {
        toast.error("Select an administrator profile before installing a plugin.");
      }
    },
    mutateAsync: (body: InstallPluginRequest) => mutation.mutateAsync(captureInstall(body)),
  };
}

export interface UploadPluginRequest {
  file: File;
  onProgress?: (progress: ChunkedUploadProgress) => void;
}

type PluginUploadIntent = UploadPluginRequest & { profileContext: ProfileRequestContextSnapshot };
export function useUploadPlugin() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ file, onProgress, profileContext }: PluginUploadIntent) => {
      const row = await uploadAdminPlugin({ file, onProgress, profileContext });
      return pluginInstallationOfV2(row);
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin uploaded");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        lifecycleFailure(
          error,
          "Plugin upload could not be confirmed. Refresh installations before uploading again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (
      request: UploadPluginRequest,
      options?: { onSuccess?: () => void; onError?: () => void },
    ) => {
      const profileContext = captureProfileRequestContext();
      if (!profileContext) {
        toast.error("Select an administrator profile before uploading a plugin.");
        options?.onError?.();
        return;
      }
      mutation.mutate({ ...request, profileContext }, options);
    },
  };
}

/**
 * Wraps {@link useUploadPlugin} with the upload-progress state and reset wiring
 * shared by the admin plugin upload forms.
 */
export function usePluginUpload() {
  const uploadPlugin = useUploadPlugin();
  const [progress, setProgress] = useState<number | null>(null);

  const upload = useCallback(
    (file: File, options?: { onSuccess?: () => void }) => {
      setProgress(0);
      uploadPlugin.mutate(
        { file, onProgress: (next) => setProgress(next.percent) },
        {
          onSuccess: () => {
            setProgress(null);
            options?.onSuccess?.();
          },
          onError: () => setProgress(null),
        },
      );
    },
    [uploadPlugin],
  );

  return { upload, progress, isPending: uploadPlugin.isPending };
}

type PluginInstallationUpdateIntent = PluginLifecycleIntent & {
  body: UpdatePluginInstallationRequest;
};
export function useUpdatePluginInstallation() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ id, body, profileContext }: PluginInstallationUpdateIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const row = await v2("PUT /api/v2/admin/plugins/installations/{id}", {
        path: { id: String(id) },
        body: {
          enabled: body.enabled,
          update_policy: body.update_policy as "auto" | "notify" | "off" | "manual" | undefined,
        },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return pluginInstallationOfV2(row);
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(lifecycleFailure(error, "Failed to update plugin"));
    },
  });
  return {
    ...mutation,
    mutate: ({ id, body }: { id: number; body: UpdatePluginInstallationRequest }) => {
      try {
        mutation.mutate({ ...captureInstallation(id), body: { ...body } });
      } catch {
        toast.error("Select an administrator profile before updating a plugin.");
      }
    },
  };
}

export function useApplyPluginUpdate() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ id, profileContext }: PluginLifecycleIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const row = await v2("POST /api/v2/admin/plugins/installations/{id}/update", {
        path: { id: String(id) },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return pluginInstallationOfV2(row);
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        lifecycleFailure(
          error,
          "Plugin update could not be confirmed. Refresh installations before submitting again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (id: number) => {
      try {
        mutation.mutate(captureInstallation(id));
      } catch {
        toast.error("Select an administrator profile before updating a plugin.");
      }
    },
  };
}

export function useRestartPluginInstallation() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ id, profileContext }: PluginLifecycleIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const row = await v2("POST /api/v2/admin/plugins/installations/{id}/restart", {
        path: { id: String(id) },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return pluginInstallationOfV2(row);
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin restart requested");
      invalidatePluginQueries(queryClient);
      void queryClient.invalidateQueries({ queryKey: adminKeys.networkAccessStatusRoot() });
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        lifecycleFailure(
          error,
          "Plugin restart could not be confirmed. Refresh installations before trying again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (id: number) => {
      try {
        mutation.mutate(captureInstallation(id));
      } catch {
        toast.error("Select an administrator profile before restarting a plugin.");
      }
    },
  };
}

export function useDeletePluginInstallation() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    retry: false,
    mutationFn: async ({ id, profileContext }: PluginLifecycleIntent) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("DELETE /api/v2/admin/plugins/installations/{id}", {
        path: { id: String(id) },
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin removed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        lifecycleFailure(
          error,
          "Plugin removal could not be confirmed. Refresh installations before submitting again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (id: number) => {
      try {
        mutation.mutate(captureInstallation(id));
      } catch {
        toast.error("Select an administrator profile before removing a plugin.");
      }
    },
  };
}

export function useCheckPluginUpdates() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: () =>
      v2("POST /api/v2/admin/tasks/{key}/run", {
        path: { key: CHECK_PLUGIN_UPDATES_TASK_KEY },
        retryAuthentication: false,
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.tasks() });
      queryClient.invalidateQueries({ queryKey: adminKeys.task(CHECK_PLUGIN_UPDATES_TASK_KEY) });
      invalidatePluginQueries(queryClient);
      toast.success("Plugin update check started");
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to start plugin update check");
    },
  });
}

type PluginInstallationIntent<Body> = {
  id: number;
  body: Body;
  profileContext: ProfileRequestContextSnapshot;
};
function captureInstallationIntent<Body extends object>(
  id: number,
  body: Body,
): PluginInstallationIntent<Body> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  // Nested values (config maps, triggers) are copied too, so an edit to the
  // dialog draft after queueing cannot change the body that is sent.
  return { id, body: structuredClone(body), profileContext };
}
function mutationFailureMessage(error: unknown, fallback: string): string {
  if (error instanceof StaleApiRequestContextError)
    return "Select an administrator profile before changing plugin settings.";
  if (error instanceof V2ProblemError && (error.status === 422 || error.status === 409))
    return error.message;
  return fallback;
}

export function useSavePluginConfig() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      id,
      body,
      profileContext,
    }: PluginInstallationIntent<SavePluginConfigRequest>) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("PUT /api/v2/admin/plugins/installations/{id}/config", {
        path: { id: String(id) },
        body,
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    retry: false,
    onSuccess: async (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Plugin config saved");
      await invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        mutationFailureMessage(
          error,
          "Plugin config could not be confirmed. Reload the plugin before saving again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (input: { id: number; body: SavePluginConfigRequest }) => {
      try {
        mutation.mutate(captureInstallationIntent(input.id, input.body));
      } catch {
        toast.error("Select an administrator profile before saving plugin config.");
      }
    },
    mutateAsync: (input: { id: number; body: SavePluginConfigRequest }) =>
      mutation.mutateAsync(captureInstallationIntent(input.id, input.body)),
  };
}

export function useTestPluginConfig() {
  const mutation = useMutation({
    mutationFn: async ({
      id,
      body,
      profileContext,
    }: PluginInstallationIntent<SavePluginConfigRequest>): Promise<ConnectionCheckResponse> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2("POST /api/v2/admin/plugins/installations/{id}/config/test", {
        path: { id: String(id) },
        body,
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
    mutate: (input: { id: number; body: SavePluginConfigRequest }) =>
      mutation.mutate(captureInstallationIntent(input.id, input.body)),
    mutateAsync: (input: { id: number; body: SavePluginConfigRequest }) =>
      mutation.mutateAsync(captureInstallationIntent(input.id, input.body)),
  };
}

export function useSavePluginAuthBinding() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      id,
      body,
      profileContext,
    }: PluginInstallationIntent<SavePluginAuthBindingRequest>) => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      await v2("PUT /api/v2/admin/plugins/installations/{id}/auth-binding", {
        path: { id: String(id) },
        body,
        profileContext,
        retryAuthentication: false,
      });
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
    },
    retry: false,
    onSuccess: (_result, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success("Auth binding saved — restart the server to apply it");
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        mutationFailureMessage(
          error,
          "Auth binding could not be confirmed. Reload the plugin before saving again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (input: { id: number; body: SavePluginAuthBindingRequest }) => {
      try {
        mutation.mutate(captureInstallationIntent(input.id, input.body));
      } catch {
        toast.error("Select an administrator profile before saving an auth binding.");
      }
    },
    mutateAsync: (input: { id: number; body: SavePluginAuthBindingRequest }) =>
      mutation.mutateAsync(captureInstallationIntent(input.id, input.body)),
  };
}

type PluginTaskBindingIntent = PluginInstallationIntent<SavePluginTaskBindingRequest> & {
  capabilityId: string;
};
function captureTaskBindingIntent(input: {
  id: number;
  capabilityId: string;
  body: SavePluginTaskBindingRequest;
}): PluginTaskBindingIntent {
  return { ...captureInstallationIntent(input.id, input.body), capabilityId: input.capabilityId };
}

export function useSavePluginTaskBinding() {
  const queryClient = useQueryClient();
  const mutation = useMutation({
    mutationFn: async ({
      id,
      capabilityId,
      body,
      profileContext,
    }: PluginTaskBindingIntent): Promise<PluginTaskBindingUpdateResponse> => {
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      const result = await v2(
        "PUT /api/v2/admin/plugins/installations/{id}/task-bindings/{capability_id}",
        {
          path: { id: String(id), capability_id: capabilityId },
          body,
          profileContext,
          retryAuthentication: false,
        },
      );
      if (!isCapturedProfileAuthorityActive(profileContext))
        throw new StaleApiRequestContextError();
      return result;
    },
    retry: false,
    onSuccess: (data, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.success(
        data.restart_required
          ? "Task binding saved — restart the server to apply it"
          : "Task binding saved",
      );
      invalidatePluginQueries(queryClient);
    },
    onError: (error, intent) => {
      if (!isCapturedProfileAuthorityActive(intent.profileContext)) return;
      toast.error(
        mutationFailureMessage(
          error,
          "Task binding could not be confirmed. Reload the plugin before saving again.",
        ),
      );
    },
  });
  return {
    ...mutation,
    mutate: (input: { id: number; capabilityId: string; body: SavePluginTaskBindingRequest }) => {
      try {
        mutation.mutate(captureTaskBindingIntent(input));
      } catch {
        toast.error("Select an administrator profile before saving a task binding.");
      }
    },
    mutateAsync: (input: {
      id: number;
      capabilityId: string;
      body: SavePluginTaskBindingRequest;
    }) => mutation.mutateAsync(captureTaskBindingIntent(input)),
  };
}
