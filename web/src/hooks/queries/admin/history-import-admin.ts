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
import { t } from "@/i18n";

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
      toast.success(t("web.admin.historyImport.toast.mappingCreated"));
      queryClient.invalidateQueries({
        queryKey: adminKeys.historyImportMappings(variables.source_id),
      });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.mappingCreateFailed"),
      );
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
      toast.success(t("web.admin.historyImport.toast.mappingUpdated"));
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.mappingUpdateFailed"),
      );
    },
  });
}

export function useDeleteAdminMapping() {
  const queryClient = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: ({ id, etag }: { id: number; etag?: string }) => deleteAdminImportMapping(id, etag),
    onSuccess: () => {
      toast.success(t("web.admin.historyImport.toast.mappingDeleted"));
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.mappingDeleteFailed"),
      );
    },
  });
}

// --- Runs ---

/** A mapping or source to import, with an optional lookback window in hours. */
export type AdminRunStart = { id: number; lookbackHours?: number };

export function useCreateAdminRunForMapping() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: ({ id, lookbackHours }: AdminRunStart) => createAdminImportRun(id, lookbackHours),
    onSuccess: (run) => {
      queryClient.setQueryData([...adminKeys.historyImportAdminRun(run.id), scope], run);
      toast.success(t("web.admin.historyImport.toast.importQueued"));
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.importStartFailed"),
      );
    },
  });
}

export function useAdminBulkRun() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: ({ id, lookbackHours }: AdminRunStart) => bulkAdminImportRuns(id, lookbackHours),
    onSuccess: (data) => {
      for (const outcome of data.outcomes)
        if (outcome.run)
          queryClient.setQueryData(
            [...adminKeys.historyImportAdminRun(outcome.run.id), scope],
            outcome.run,
          );
      toast.success(
        t("web.admin.historyImport.toast.bulkQueued", {
          accepted: data.accepted,
          active: data.active,
          failed: data.failed,
        }),
      );
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportMappings"] });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.bulkStartFailed"),
      );
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
        throw new Error(t("web.admin.historyImport.toast.profileChanged"));
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

export function useCancelAdminRun() {
  const queryClient = useQueryClient();
  const scope = adminImportScope();
  return useMutation({
    retry: false,
    mutationFn: cancelAdminImportRun,
    onSuccess: (run) => {
      queryClient.setQueryData([...adminKeys.historyImportAdminRun(run.id), scope], run);
      toast.success(
        importRunActive(run)
          ? t("web.admin.historyImport.toast.cancelRequested")
          : t("web.admin.historyImport.toast.runCanceled"),
      );
      queryClient.invalidateQueries({ queryKey: ["admin", "historyImportAdminRuns"] });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : t("web.admin.historyImport.toast.cancelFailed"),
      );
    },
  });
}
