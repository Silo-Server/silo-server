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
      toast.success("Saved server created");
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create saved server");
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
      toast.success("Saved server updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update saved server");
    },
  });
}

export function useDeleteAdminHistoryImportSource() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => deleteAdminImportSource(id, etag),
    onSuccess: () => {
      toast.success("Saved server deleted");
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
      queryClient.invalidateQueries({ queryKey: historyImportKeys.sources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete saved server");
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
      toast.success("Admin token saved");
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save admin token");
    },
  });
}

export function useClearAdminSourceToken() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => clearAdminImportToken(id, etag),
    onSuccess: () => {
      toast.success("Admin token removed");
      queryClient.invalidateQueries({ queryKey: adminKeys.historyImportSources() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove admin token");
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
      toast.error(err instanceof Error ? err.message : "Plex login failed");
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
