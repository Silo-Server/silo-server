import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useState } from "react";
import { toast } from "sonner";

import { api } from "@/api/client";
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
import {
  DEFAULT_UPLOAD_CHUNK_SIZE,
  type ChunkedUploadProgress,
  uploadFileInChunks,
} from "@/lib/chunkedUpload";
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
  return useQuery({
    queryKey: adminKeys.pluginInstallations(),
    queryFn: () =>
      api<PluginInstallation[]>("/admin/plugins/installations").then((data) => data ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminPlugins() {
  const repositoriesQuery = useQuery({
    queryKey: adminKeys.pluginRepositories(),
    queryFn: () =>
      api<PluginRepository[]>("/admin/plugins/repositories").then((data) => data ?? []),
    staleTime: ADMIN_STALE_TIME,
  });

  const catalogQuery = useQuery({
    queryKey: adminKeys.pluginCatalog(),
    queryFn: () => api<PluginCatalogEntry[]>("/admin/plugins/catalog").then((data) => data ?? []),
    staleTime: ADMIN_STALE_TIME,
  });

  const installationsQuery = useQuery({
    queryKey: adminKeys.pluginInstallations(),
    queryFn: () =>
      api<PluginInstallation[]>("/admin/plugins/installations").then((data) => data ?? []),
    staleTime: ADMIN_STALE_TIME,
  });

  const catalogSettingsQuery = useQuery({
    queryKey: adminKeys.pluginCatalogSettings(),
    queryFn: () => api<PluginCatalogSettings>("/admin/plugins/catalog-settings"),
    staleTime: ADMIN_STALE_TIME,
  });

  return {
    repositories: repositoriesQuery.data ?? [],
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

export function useUpdatePluginCatalogSettings() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UpdatePluginCatalogSettingsRequest) =>
      api<PluginCatalogSettings>("/admin/plugins/catalog-settings", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Plugin catalog settings updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(
        error instanceof Error ? error.message : "Failed to update plugin catalog settings",
      );
    },
  });
}

export function useCreatePluginRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreatePluginRepositoryRequest) =>
      api<PluginRepository>("/admin/plugins/repositories", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Repository added");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to add repository");
    },
  });
}

export function useUpdatePluginRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: UpdatePluginRepositoryRequest }) =>
      api<PluginRepository>(`/admin/plugins/repositories/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Repository updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to update repository");
    },
  });
}

export function useDeletePluginRepository() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/plugins/repositories/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Repository removed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to remove repository");
    },
  });
}

export function useInstallPlugin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: InstallPluginRequest) =>
      api<PluginInstallation>("/admin/plugins/installations", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Plugin installed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to install plugin");
    },
  });
}

export interface UploadPluginRequest {
  file: File;
  onProgress?: (progress: ChunkedUploadProgress) => void;
}

export function useUploadPlugin() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ file, onProgress }: UploadPluginRequest) => {
      if (file.size > DEFAULT_UPLOAD_CHUNK_SIZE) {
        return uploadFileInChunks<PluginInstallation>({
          file,
          createPath: "/admin/plugins/uploads/chunked",
          chunkPath: (uploadId, chunkIndex) =>
            `/admin/plugins/uploads/chunked/${encodeURIComponent(uploadId)}/chunks/${chunkIndex}`,
          completePath: (uploadId) =>
            `/admin/plugins/uploads/chunked/${encodeURIComponent(uploadId)}/complete`,
          cancelPath: (uploadId) =>
            `/admin/plugins/uploads/chunked/${encodeURIComponent(uploadId)}`,
          onProgress,
        });
      }

      const formData = new FormData();
      formData.append("archive", file);
      return api<PluginInstallation>("/admin/plugins/uploads", {
        method: "POST",
        body: formData,
      });
    },
    onSuccess: () => {
      toast.success("Plugin uploaded");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to upload plugin");
    },
  });
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

export function useUpdatePluginInstallation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: UpdatePluginInstallationRequest }) =>
      api<PluginInstallation>(`/admin/plugins/installations/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Plugin updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to update plugin");
    },
  });
}

export function useApplyPluginUpdate() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api<PluginInstallation>(`/admin/plugins/installations/${id}/update`, {
        method: "POST",
      }),
    onSuccess: () => {
      toast.success("Plugin updated");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to update plugin");
    },
  });
}

export function useDeletePluginInstallation() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/admin/plugins/installations/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Plugin removed");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to remove plugin");
    },
  });
}

export function useCheckPluginUpdates() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: () =>
      api<{ status: string }>(
        `/admin/tasks/${encodeURIComponent(CHECK_PLUGIN_UPDATES_TASK_KEY)}/run`,
        {
          method: "POST",
        },
      ),
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

export function useSavePluginConfig() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SavePluginConfigRequest }) =>
      api(`/admin/plugins/installations/${id}/config`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: async () => {
      toast.success("Plugin config saved");
      await invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save plugin config");
    },
  });
}

export function useTestPluginConfig() {
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SavePluginConfigRequest }) =>
      api<ConnectionCheckResponse>(`/admin/plugins/installations/${id}/config/test`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
  });
}

export function useSavePluginAuthBinding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SavePluginAuthBindingRequest }) =>
      api(`/admin/plugins/installations/${id}/auth-binding`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Auth binding saved — restart the server to apply it");
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save auth binding");
    },
  });
}

export function useSavePluginTaskBinding() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      id,
      capabilityId,
      body,
    }: {
      id: number;
      capabilityId: string;
      body: SavePluginTaskBindingRequest;
    }) =>
      api<PluginTaskBindingUpdateResponse>(
        `/admin/plugins/installations/${id}/task-bindings/${capabilityId}`,
        {
          method: "PUT",
          body: JSON.stringify(body),
        },
      ),
    onSuccess: (data) => {
      toast.success(
        data.restart_required
          ? "Task binding saved — restart the server to apply it"
          : "Task binding saved",
      );
      invalidatePluginQueries(queryClient);
    },
    onError: (error) => {
      toast.error(error instanceof Error ? error.message : "Failed to save task binding");
    },
  });
}
