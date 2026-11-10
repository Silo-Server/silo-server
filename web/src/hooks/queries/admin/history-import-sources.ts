import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  adminImportScope,
  adminImportCapabilities,
  listAdminImportSources,
  createAdminImportSource,
  updateAdminImportSource,
  deleteAdminImportSource,
  setAdminImportToken,
  clearAdminImportToken,
  discoverAdminImportUsers,
  plexAdminImportLogin,
} from "@/api/v2/adminHistoryImports";
import type {
  CreateHistoryImportSourceRequest,
  SetHistoryImportAdminTokenRequest,
  UpdateHistoryImportSourceRequest,
} from "@/api/types";
import { adminKeys, historyImportKeys } from "../keys";
import { toast } from "sonner";
import { t } from "@/i18n";

export function useAdminHistoryImportSources() {
  return useQuery({
    queryKey: [...adminKeys.historyImportSources(), adminImportScope()],
    queryFn: listAdminImportSources,
    retry: false,
  });
}

export function useCreateAdminHistoryImportSource() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CreateHistoryImportSourceRequest) => createAdminImportSource(body),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.sourceCreated"));
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.sourceCreateFailed"),
      );
    },
  });
}

export function useUpdateAdminHistoryImportSource() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      body,
      etag,
    }: {
      id: number;
      body: UpdateHistoryImportSourceRequest;
      etag?: string;
    }) => updateAdminImportSource(id, body, etag),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.sourceUpdated"));
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.sourceUpdateFailed"),
      );
    },
  });
}

export function useDeleteAdminHistoryImportSource() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => deleteAdminImportSource(id, etag),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.sourceDeleted"));
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.sourceDeleteFailed"),
      );
    },
  });
}

export function useSetAdminSourceToken() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      body,
      etag,
    }: {
      id: number;
      body: SetHistoryImportAdminTokenRequest;
      etag?: string;
    }) => setAdminImportToken(id, body.token, etag),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.tokenSaved"));
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.tokenSaveFailed"),
      );
    },
  });
}

export function useClearAdminSourceToken() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => clearAdminImportToken(id, etag),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.tokenRemoved"));
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.tokenRemoveFailed"),
      );
    },
  });
}

export function useDiscoverExternalUsers(sourceId: number | undefined) {
  return useQuery({
    queryKey: [...adminKeys.historyImportExternalUsers(sourceId ?? 0), adminImportScope()],
    queryFn: () => discoverAdminImportUsers(sourceId!),
    enabled: false, // manually triggered
    retry: false,
  });
}

export function usePlexLogin() {
  return useMutation({
    retry: false,
    mutationFn: plexAdminImportLogin,
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.plexLoginFailed"),
      );
    },
  });
}

export function useAdminHistoryImportCapabilities() {
  return useQuery({
    queryKey: ["admin", "historyImportCapabilities", adminImportScope()],
    queryFn: adminImportCapabilities,
    retry: false,
  });
}
