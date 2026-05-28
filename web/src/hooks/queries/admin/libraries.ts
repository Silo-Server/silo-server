import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api, getAccessToken } from "@/api/client";
import type {
  AdminJob,
  AdminJobsResponse,
  ApiError,
  CatalogSeedExportRequest,
  CatalogSeedImportRequest,
  CatalogSeedImportResponse,
  CatalogSeedImportSourcesResponse,
  CatalogSeedImportSource,
  CreateLibraryRequest,
  DeleteLibraryRootOverrideRequest,
  Library,
  LibraryMountCheckResponse,
  LibraryRoot,
  LibraryRootsResponse,
  LibrarySkippedRoot,
  StaleMediaID,
  LibraryProviderChainResponse,
  ScanResponse,
  SetLibraryChainRequest,
  UnmatchedLibraryItemsResponse,
  UpsertLibraryRootOverrideRequest,
  FilesystemBrowseResponse,
} from "@/api/types";
import { adminKeys, libraryKeys } from "../keys";
import { toast } from "sonner";
import type { LibraryReorderEntry } from "@/pages/adminLibraryOrder";

const ADMIN_STALE_TIME = 30_000;

class AdminJobRequestError extends Error {
  status?: number;
  unmatchedRoots?: string[];
  activeJobId?: string;
  activeJob?: AdminJob;

  constructor(
    message: string,
    status?: number,
    unmatchedRoots?: string[],
    activeJobId?: string,
    activeJob?: AdminJob,
  ) {
    super(message);
    this.name = "AdminJobRequestError";
    this.status = status;
    this.unmatchedRoots = unmatchedRoots;
    this.activeJobId = activeJobId;
    this.activeJob = activeJob;
  }
}

function buildAdminHeaders() {
  const headers: Record<string, string> = {};
  const token = getAccessToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  return headers;
}

async function parseAdminJobError(res: Response): Promise<never> {
  let apiErr: ApiError = { error: "unknown", message: res.statusText };
  try {
    apiErr = (await res.json()) as ApiError;
  } catch {
    // Ignore JSON parse failures for non-JSON error bodies.
  }
  throw new AdminJobRequestError(
    apiErr.message || "Admin job request failed",
    res.status,
    apiErr.unmatched_roots,
    apiErr.active_job_id,
    apiErr.active_job,
  );
}

async function createCatalogExportJob(body?: CatalogSeedExportRequest): Promise<AdminJob> {
  const res = await fetch("/api/v1/admin/catalog/export-jobs", {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...buildAdminHeaders(),
    },
    body: JSON.stringify(body ?? {}),
  });

  if (!res.ok) {
    await parseAdminJobError(res);
  }

  return (await res.json()) as AdminJob;
}

async function createCatalogImportJob(body: CatalogSeedImportRequest): Promise<AdminJob> {
  const form = new FormData();
  if (body.source === "local_path" && body.local_path) {
    form.append("local_path", body.local_path);
  }
  if (body.source === "export_job" && body.export_job_id) {
    form.append("export_job_id", body.export_job_id);
  }
  if (body.source === "bucket_artifact" && body.artifact_key) {
    form.append("artifact_key", body.artifact_key);
  }
  if (body.source === "remote_url" && body.remote_url) {
    form.append("remote_url", body.remote_url);
  }
  form.append("conflict_mode", body.conflict_mode);
  form.append("path_rewrites", JSON.stringify(body.path_rewrites));

  const res = await fetch("/api/v1/admin/catalog/import-jobs", {
    method: "POST",
    headers: buildAdminHeaders(),
    body: form,
  });

  if (!res.ok) {
    await parseAdminJobError(res);
  }

  return (await res.json()) as AdminJob;
}

async function importCatalogSeed(
  body: CatalogSeedImportRequest,
): Promise<CatalogSeedImportResponse> {
  const form = new FormData();
  if (body.source === "local_path" && body.local_path) {
    form.append("local_path", body.local_path);
  }
  if (body.source === "export_job" && body.export_job_id) {
    form.append("export_job_id", body.export_job_id);
  }
  if (body.source === "bucket_artifact" && body.artifact_key) {
    form.append("artifact_key", body.artifact_key);
  }
  if (body.source === "remote_url" && body.remote_url) {
    form.append("remote_url", body.remote_url);
  }
  form.append("conflict_mode", body.conflict_mode);
  form.append("path_rewrites", JSON.stringify(body.path_rewrites));

  const res = await fetch("/api/v1/admin/catalog/import", {
    method: "POST",
    headers: buildAdminHeaders(),
    body: form,
  });

  if (!res.ok) {
    await parseAdminJobError(res);
  }

  return (await res.json()) as CatalogSeedImportResponse;
}

async function listCatalogImportSources(): Promise<CatalogSeedImportSource[]> {
  return api<CatalogSeedImportSourcesResponse>("/admin/catalog/import-sources").then(
    (data) => data.sources ?? [],
  );
}

async function listLocalImportSources(): Promise<CatalogSeedImportSource[]> {
  return api<CatalogSeedImportSourcesResponse>("/admin/catalog/local-import-sources").then(
    (data) => data.sources ?? [],
  );
}

async function publishCatalogExportJob(id: string): Promise<AdminJob> {
  const res = await fetch(`/api/v1/admin/catalog/export-jobs/${encodeURIComponent(id)}/publish`, {
    method: "POST",
    headers: buildAdminHeaders(),
  });

  if (!res.ok) {
    await parseAdminJobError(res);
  }

  return (await res.json()) as AdminJob;
}

export function useAdminLibraries() {
  return useQuery({
    queryKey: adminKeys.libraries(),
    queryFn: () => api<Library[]>("/libraries").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useReorderLibraries() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (entries: LibraryReorderEntry[]) =>
      api<void>("/libraries/reorder", {
        method: "PUT",
        body: JSON.stringify({ entries }),
      }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
      queryClient.invalidateQueries({ queryKey: libraryKeys.all });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to reorder libraries");
    },
  });
}

export function useSkippedLibraryRoots() {
  return useQuery({
    queryKey: adminKeys.librarySkippedRoots(),
    queryFn: () => api<LibrarySkippedRoot[]>("/libraries/skipped-roots").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useLibraryRoots(libraryId?: number, state?: string) {
  return useQuery({
    queryKey: adminKeys.libraryRoots(libraryId, state),
    queryFn: () => {
      if (!libraryId) return Promise.resolve([] as LibraryRoot[]);
      const params = new URLSearchParams({ library_id: String(libraryId) });
      if (state) params.set("state", state);
      return api<LibraryRootsResponse>(`/libraries/roots?${params.toString()}`).then(
        (d) => d.items ?? [],
      );
    },
    enabled: !!libraryId,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useUpsertLibraryRootOverride() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UpsertLibraryRootOverrideRequest) =>
      api<void>("/libraries/roots/override", {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryRoots(variables.library_id) });
      toast.success("Root override saved");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save root override");
    },
  });
}

export function useDeleteLibraryRootOverride() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: DeleteLibraryRootOverrideRequest) =>
      api<void>("/libraries/roots/override", {
        method: "DELETE",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryRoots(variables.library_id) });
      toast.success("Root override removed");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove root override");
    },
  });
}

export function useStaleMediaIDs() {
  return useQuery({
    queryKey: adminKeys.staleMediaIDs(),
    queryFn: () => api<StaleMediaID[]>("/libraries/stale-ids").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useRematchStaleMediaID() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (contentId: string) =>
      api(`/libraries/stale-ids/${contentId}/rematch`, { method: "POST" }),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: adminKeys.staleMediaIDs() });
      toast.success("Re-match started");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Re-match failed");
    },
  });
}

export function useCreateLibrary() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CreateLibraryRequest) =>
      api<Library>("/libraries", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Library created");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useUpdateLibrary() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: Partial<CreateLibraryRequest> }) =>
      api(`/libraries/${id}`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      toast.success("Library updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to save");
    },
  });
}

export function useDeleteLibrary() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api<AdminJob>(`/libraries/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Library deletion started");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("delete_library") });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to delete");
    },
  });
}

export function useScanLibrary() {
  return useMutation({
    mutationFn: (id: number) =>
      api<ScanResponse>("/scan", {
        method: "POST",
        body: JSON.stringify({ library_id: id }),
      }),
    onSuccess: () => {
      toast.success("Full ingest scan started");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Scan failed");
    },
  });
}

export function useCheckLibraryMount() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api<LibraryMountCheckResponse>(`/libraries/${id}/check-mount`, { method: "POST" }),
    onSuccess: (data) => {
      toast.success(data.healthy ? "Mount check passed" : "Mount check found unreachable roots");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Mount check failed");
    },
  });
}

export function useScanAllLibraries() {
  return useMutation({
    mutationFn: () =>
      api<{ status: string }>("/admin/tasks/scan_libraries/run", {
        method: "POST",
      }),
    onSuccess: () => {
      toast.success("Full ingest scan started for all libraries");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Scan failed");
    },
  });
}

export function useCancelLibraryScans() {
  return useMutation({
    mutationFn: (id: number) =>
      api<{ cancelled: number; library_id: number }>("/scan/cancel", {
        method: "POST",
        body: JSON.stringify({ library_id: id }),
      }),
    onSuccess: () => {
      toast.success("Scan cancellation requested");
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel scans");
    },
  });
}

export function useLibraryProviders(libraryId: number | null) {
  return useQuery({
    queryKey: adminKeys.libraryProviders(libraryId ?? 0),
    queryFn: () =>
      api<LibraryProviderChainResponse>(`/libraries/${libraryId}/providers`).then(
        (d) => d ?? { levels: {} },
      ),
    enabled: libraryId !== null,
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useSetLibraryProviders() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SetLibraryChainRequest }) =>
      api(`/libraries/${id}/providers`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: (_data, variables) => {
      toast.success("Provider chain updated");
      queryClient.invalidateQueries({
        queryKey: adminKeys.libraryProviders(variables.id),
      });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to update provider chain");
    },
  });
}

export function useUploadLibraryPoster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async ({ id, file }: { id: number; file: File }) => {
      const form = new FormData();
      form.append("poster", file);
      const res = await fetch(`/api/v1/libraries/${id}/poster`, {
        method: "PUT",
        headers: buildAdminHeaders(),
        body: form,
      });
      if (!res.ok) {
        const err = await res.json().catch(() => ({ message: res.statusText }));
        throw new Error(err.message || "Failed to upload poster");
      }
      return (await res.json()) as Library;
    },
    onSuccess: () => {
      toast.success("Library poster updated");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to upload poster");
    },
  });
}

export function useDeleteLibraryPoster() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => api(`/libraries/${id}/poster`, { method: "DELETE" }),
    onSuccess: () => {
      toast.success("Library poster removed");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to remove poster");
    },
  });
}

export function useRefreshLibraryMetadata() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: number) => {
      const res = await fetch(`/api/v1/libraries/${id}/refresh-metadata`, {
        method: "POST",
        headers: buildAdminHeaders(),
      });

      if (!res.ok) {
        await parseAdminJobError(res);
      }

      return (await res.json()) as AdminJob;
    },
    onSuccess: () => {
      toast.success("Metadata refresh queued");
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("library_refresh") });
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
    },
    onError: (err) => {
      if (err instanceof AdminJobRequestError && err.activeJobId) {
        toast.error(err.message);
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("library_refresh") });
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
        return;
      }
      toast.error(err instanceof Error ? err.message : "Refresh failed");
    },
  });
}

const UNMATCHED_PAGE_SIZE = 10;

export function useUnmatchedLibraryItems(page = 0, search = "") {
  const offset = page * UNMATCHED_PAGE_SIZE;
  const trimmed = search.trim();
  return useQuery({
    queryKey: adminKeys.unmatchedItems(page, trimmed),
    queryFn: () =>
      api<UnmatchedLibraryItemsResponse>(
        `/libraries/unmatched-items?limit=${UNMATCHED_PAGE_SIZE}&offset=${offset}${
          trimmed ? `&q=${encodeURIComponent(trimmed)}` : ""
        }`,
      ).then((d) => d ?? { items: [], total: 0 }),
    staleTime: ADMIN_STALE_TIME,
    placeholderData: (prev) => prev,
  });
}

export { UNMATCHED_PAGE_SIZE };

export function useConfirmEmptyRootCleanup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      api(`/libraries/${id}/confirm-empty-root-cleanup`, { method: "POST" }),
    onSuccess: () => {
      toast.success("Deletion confirmed for the next empty-root scan");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to confirm cleanup");
    },
  });
}

export function useCatalogExportJobs(jobType = "catalog_export") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: () =>
      api<AdminJobsResponse>(`/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=10`).then(
        (data) => data.jobs ?? [],
      ),
    staleTime: 0,
  });
}

export function useCatalogImportJobs(jobType = "catalog_import") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: () =>
      api<AdminJobsResponse>(`/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=10`).then(
        (data) => data.jobs ?? [],
      ),
    staleTime: 0,
  });
}

export function useLibraryDeleteJobs(jobType = "delete_library") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: () =>
      api<AdminJobsResponse>(`/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=20`).then(
        (data) => data.jobs ?? [],
      ),
    staleTime: 0,
  });
}

export function useLibraryRefreshJobs(jobType = "library_refresh") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: () =>
      api<AdminJobsResponse>(`/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=50`).then(
        (data) => data.jobs ?? [],
      ),
    staleTime: 0,
  });
}

export function useAllAdminJobs(limit = 30) {
  return useQuery({
    queryKey: adminKeys.jobs("__all"),
    queryFn: () =>
      api<AdminJobsResponse>(`/admin/jobs?limit=${limit}`).then((data) => data.jobs ?? []),
    staleTime: 0,
  });
}

export function useCatalogImportSources() {
  return useQuery({
    queryKey: adminKeys.catalogImportSources(),
    queryFn: listCatalogImportSources,
    staleTime: 0,
    refetchInterval: 30_000,
  });
}

export function useLocalImportSources() {
  return useQuery({
    queryKey: adminKeys.localImportSources(),
    queryFn: listLocalImportSources,
    staleTime: 0,
    refetchInterval: 30_000,
  });
}

export function useCreateCatalogExportJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body?: CatalogSeedExportRequest) => createCatalogExportJob(body),
    onSuccess: () => {
      toast.success("Catalog export queued");
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("catalog_export") });
    },
    onError: (err) => {
      if (err instanceof AdminJobRequestError && err.activeJobId) {
        toast.error(err.message);
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("catalog_export") });
        return;
      }
      toast.error(err instanceof Error ? err.message : "Failed to queue catalog export");
    },
  });
}

export function usePublishCatalogExportJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => publishCatalogExportJob(id),
    onSuccess: () => {
      toast.success("Catalog export published");
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("catalog_export") });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to publish catalog export");
    },
  });
}

export function useImportCatalogSeed() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (body: CatalogSeedImportRequest) => {
      try {
        const job = await createCatalogImportJob(body);
        return { mode: "job" as const, job };
      } catch (err) {
        if (
          err instanceof AdminJobRequestError &&
          (err.status === 404 ||
            (body.source !== "export_job" && err.message === "Job repository is not configured"))
        ) {
          const result = await importCatalogSeed(body);
          return { mode: "sync" as const, result };
        }
        throw err;
      }
    },
    onSuccess: (payload) => {
      if (payload.mode === "job") {
        toast.success("Catalog import queued");
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("catalog_import") });
        return;
      }
      toast.success(
        `Catalog imported: ${payload.result.items_created} items, ${payload.result.files_created} files`,
      );
      queryClient.invalidateQueries({ queryKey: adminKeys.libraries() });
    },
    onError: (err) => {
      if (err instanceof AdminJobRequestError && err.unmatchedRoots?.length) {
        toast.error(
          `${err.message}: ${err.unmatchedRoots.slice(0, 2).join(", ")}${err.unmatchedRoots.length > 2 ? "..." : ""}`,
        );
        return;
      }
      toast.error(err instanceof Error ? err.message : "Failed to import catalog seed");
    },
  });
}

export function useFilesystemBrowse(path: string) {
  return useFilesystemBrowseWhen(path, true);
}

export function useFilesystemBrowseWhen(path: string, enabled: boolean) {
  return useQuery({
    queryKey: adminKeys.filesystemBrowse(path),
    queryFn: () => fetchFilesystemBrowse(path),
    staleTime: 60_000,
    enabled: enabled && path.trim().length > 0,
  });
}

export function fetchFilesystemBrowse(path: string) {
  return api<FilesystemBrowseResponse>(`/admin/filesystem/browse?path=${encodeURIComponent(path)}`);
}
