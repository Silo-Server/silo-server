import { useMutation, useQuery, useQueryClient, useInfiniteQuery } from "@tanstack/react-query";
import {
  adminImportScope,
  importAuthority,
  listAdminImportMappings,
  createAdminImportMapping,
  updateAdminImportMapping,
  deleteAdminImportMapping,
  createAdminImportRun,
  bulkAdminImportRuns,
  getAdminImportRunsPage,
  getAdminImportRun,
  cancelAdminImportRun,
  importRunActive,
  type AdminImportRun,
} from "@/api/v2/adminHistoryImports";
import type {
  CreateHistoryImportMappingRequest,
  UpdateHistoryImportMappingRequest,
} from "@/api/types";
import { adminKeys } from "../keys";
import { toast } from "sonner";

// --- Mappings ---

export function useAdminHistoryImportMappings(
  sourceId: number | undefined,
  hasActiveRuns?: boolean,
) {
  return useQuery({
    queryKey: [...adminKeys.historyImportMappings(sourceId), adminImportScope()],
    queryFn: () => listAdminImportMappings(sourceId!),
    retry: false,
    enabled: sourceId != null && sourceId > 0,
    staleTime: hasActiveRuns ? 5_000 : 30_000,
  });
}

export function useCreateAdminMapping() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: (body: CreateHistoryImportMappingRequest) => createAdminImportMapping(body),
    onSuccess: (_data, variables) => {
      toast.success("User mapping created");
      queryClient.invalidateQueries({
        queryKey: adminKeys.historyImportMappings(variables.source_id),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to create mapping");
    },
  });
}

export function useUpdateAdminMapping() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({
      id,
      body,
      etag,
    }: {
      id: number;
      body: UpdateHistoryImportMappingRequest;
      etag?: string;
    }) => updateAdminImportMapping(id, body, etag),
    onSuccess: () => {
      toast.success("Mapping updated");
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update mapping");
    },
  });
}

export function useDeleteAdminMapping() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => deleteAdminImportMapping(id, etag),
    onSuccess: () => {
      toast.success("Mapping deleted");
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete mapping");
    },
  });
}

// --- Runs ---

export function useCreateAdminRunForMapping() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: createAdminImportRun,
    onSuccess: (run) => {
      queryClient.setQueryData([...adminKeys.historyImportAdminRun(run.id), scope], run);
      toast.success("Import queued");
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to start import");
    },
  });
}

export function useAdminBulkRun() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: bulkAdminImportRuns,
    onSuccess: (data) => {
      for (const outcome of data.outcomes)
        if (outcome.run)
          queryClient.setQueryData(
            [...adminKeys.historyImportAdminRun(outcome.run.id), scope],
            outcome.run,
          );
      toast.success(
        `${data.accepted} queued, ${data.active} already active, ${data.failed} failed`,
      );
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to start bulk import");
    },
  });
}

export function useAdminHistoryImportRuns(sourceId?: number, enabled = true) {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  const query = useInfiniteQuery({
    enabled,
    queryKey: [
      ...adminKeys.historyImportAdminRuns(sourceId == null ? {} : { source_id: sourceId }),
      scope,
    ],
    initialPageParam: undefined as string | undefined,
    queryFn: async ({ pageParam }) => {
      const profileContext = importAuthority();
      if (adminImportScope() !== scope)
        throw new Error("The active profile changed. Reload imports.");
      const page = await getAdminImportRunsPage(sourceId, pageParam, profileContext);
      const items = await Promise.all(
        page.items.map(async (run) => {
          if (!importRunActive(run)) return run;
          const key = [...adminKeys.historyImportAdminRun(run.id), scope];
          const previous = queryClient.getQueryData<AdminImportRun>(key);
          const latest = await getAdminImportRun(run.id, previous?.location, profileContext);
          queryClient.setQueryData(key, latest);
          return latest;
        }),
      );
      return { ...page, items };
    },
    getNextPageParam: (last, _pages, _lastParam, params) =>
      last.nextCursor && !params.includes(last.nextCursor) ? last.nextCursor : undefined,
    retry: false,
    staleTime: 5000,
    refetchInterval: (query) => {
      if (query.state.status === "error") return false;
      const active = (query.state.data?.pages.flatMap((page) => page.items) ?? []).filter(
        importRunActive,
      );
      return active.length ? Math.min(...active.map((run) => run.retryAfterMs ?? 5000)) : false;
    },
  });
  return {
    data: query.data?.pages.flatMap((page) => page.items),
    error: query.error,
    hasNextPage: query.hasNextPage,
    fetchNextPage: query.fetchNextPage,
    isFetchingNextPage: query.isFetchingNextPage,
  };
}
export function useAdminHistoryImportRun(id: string | undefined) {
  const queryClient = useQueryClient();
  const key = [...adminKeys.historyImportAdminRun(id), adminImportScope()];
  return useQuery({
    queryKey: key,
    queryFn: () => getAdminImportRun(id!, queryClient.getQueryData<AdminImportRun>(key)?.location),
    enabled: !!id,
    retry: false,
    refetchInterval: (query) =>
      query.state.status !== "error" && query.state.data && importRunActive(query.state.data)
        ? (query.state.data.retryAfterMs ?? 5000)
        : false,
  });
}

export function useCancelAdminRun() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: cancelAdminImportRun,
    onSuccess: (run) => {
      queryClient.setQueryData([...adminKeys.historyImportAdminRun(run.id), scope], run);
      toast.success(importRunActive(run) ? "Cancellation requested" : "Run canceled");
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel run");
    },
  });
}
