import { useInfiniteQuery, useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
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
  LibraryMetadataMatchFailureDetail,
  LibraryMetadataMatchQueueStatus,
  LibraryMountCheckResponse,
  LibraryRoot,
  LibrarySkippedRoot,
  StaleMediaID,
  LibraryProviderChainResponse,
  ScanResponse,
  SetLibraryChainRequest,
  UnmatchedLibraryItem,
  UpsertLibraryRootOverrideRequest,
  FilesystemBrowseResponse,
} from "@/api/types";
import {
  adminJobFromV2,
  librariesFromV2,
  libraryCreateToV2,
  libraryFromV2,
  libraryRootFromV2,
  metadataMatchQueueStatusFromV2,
  mountCheckFromV2,
  providerChainFromV2,
  providerChainToV2,
  skippedRootFromV2,
  staleMediaIDFromV2,
  unmatchedItemFromV2,
} from "@/api/v2/libraries";
import { v2, V2ProblemError, type V2Body, type V2Result } from "@/api/v2/request";
import { adminKeys, libraryKeys } from "../keys";
import { toast } from "sonner";
import type { LibraryReorderEntry } from "@/pages/adminLibraryOrder";
import { usePageActivity } from "@/hooks/usePageActivity";

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

  const imported: CatalogSeedImportResponse = await res.json();
  return imported;
}

async function listCatalogImportSources(): Promise<CatalogSeedImportSource[]> {
  const data: CatalogSeedImportSourcesResponse = await api("/admin/catalog/import-sources");
  return data.sources ?? [];
}

async function listLocalImportSources(): Promise<CatalogSeedImportSource[]> {
  const data: CatalogSeedImportSourcesResponse = await api("/admin/catalog/local-import-sources");
  return data.sources ?? [];
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

export function fetchAdminLibraries(signal?: AbortSignal): Promise<Library[]> {
  return v2("GET /api/v2/libraries", { signal }).then(librariesFromV2);
}

export function useAdminLibraries() {
  return useQuery({
    queryKey: adminKeys.libraries(),
    queryFn: ({ signal }) => fetchAdminLibraries(signal),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useReorderLibraries() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (entries: LibraryReorderEntry[]) =>
      v2("POST /api/v2/libraries/reorder", {
        body: { entries: entries.map((entry) => ({ ...entry, id: String(entry.id) })) },
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
    queryFn: ({ signal }): Promise<LibrarySkippedRoot[]> =>
      v2("GET /api/v2/libraries/skipped-roots", { signal }).then((page) =>
        page.items.map(skippedRootFromV2),
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}

/** Page size of the library roots listing. */
export const LIBRARY_ROOTS_PAGE_LIMIT = 50;

export interface LibraryRootsPage {
  roots: LibraryRoot[];
  /** Cursor of the next page, or undefined on the last page. */
  nextCursor: string | undefined;
  /** Roots matching the filter across every page, for the section header. */
  total: number;
}

/**
 * Fetches one page of a library's observed roots. Every page makes the server
 * reload the library's overrides and item-group claims, so callers page on
 * demand rather than walking the whole listing up front.
 */
export async function fetchLibraryRootsPage(
  libraryId: number,
  state?: string,
  cursor?: string,
  signal?: AbortSignal,
): Promise<LibraryRootsPage> {
  const page = await v2("GET /api/v2/libraries/roots", {
    query: {
      library_id: String(libraryId),
      limit: LIBRARY_ROOTS_PAGE_LIMIT,
      ...(state ? { state } : {}),
      ...(cursor === undefined ? {} : { cursor }),
    },
    signal,
  });
  return {
    roots: page.items.map(libraryRootFromV2),
    nextCursor: page.page?.has_more && page.page.next_cursor ? page.page.next_cursor : undefined,
    total: page.total,
  };
}

/**
 * Pages a library's observed roots by cursor. The first page loads only while
 * `enabled` holds (the diagnostics section that shows the rows is collapsed
 * by default); further pages load through `fetchNextPage`.
 */
export function useLibraryRoots(
  libraryId?: number,
  state?: string,
  { enabled = true }: { enabled?: boolean } = {},
) {
  return useInfiniteQuery({
    queryKey: adminKeys.libraryRoots(libraryId, state),
    queryFn: ({ pageParam, signal }) =>
      fetchLibraryRootsPage(libraryId ?? 0, state, pageParam, signal),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (lastPage) => lastPage.nextCursor,
    enabled: enabled && !!libraryId,
    staleTime: ADMIN_STALE_TIME,
  });
}

/** Flattens the loaded pages of useLibraryRoots into one list. */
export function flattenLibraryRoots(
  data: { pages: LibraryRootsPage[] } | undefined,
): LibraryRoot[] {
  return data?.pages.flatMap((page) => page.roots) ?? [];
}

export function useUpsertLibraryRootOverride() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ library_id, ...override }: UpsertLibraryRootOverrideRequest) =>
      v2("PUT /api/v2/libraries/roots/override", {
        body: { ...override, library_id: String(library_id) },
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
      v2("DELETE /api/v2/libraries/roots/override", {
        query: { library_id: String(body.library_id), root_path: body.root_path },
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
    queryFn: ({ signal }): Promise<StaleMediaID[]> =>
      v2("GET /api/v2/libraries/stale-ids", { signal }).then((page) =>
        page.items.map(staleMediaIDFromV2),
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useRematchStaleMediaID() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (contentId: string) =>
      v2("POST /api/v2/libraries/stale-ids/{content_id}/rematch", {
        path: { content_id: contentId },
      }),
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
    mutationFn: (body: CreateLibraryRequest): Promise<Library> =>
      v2("POST /api/v2/libraries", { body: libraryCreateToV2(body) }).then(libraryFromV2),
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
    mutationFn: ({
      id,
      body,
    }: {
      id: number;
      body: V2Body<"PATCH /api/v2/libraries/{id}">;
    }): Promise<Library> =>
      v2("PATCH /api/v2/libraries/{id}", { path: { id: String(id) }, body }).then(libraryFromV2),
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
    mutationFn: (id: number): Promise<AdminJob> =>
      v2("DELETE /api/v2/libraries/{id}", { path: { id: String(id) } }).then(adminJobFromV2),
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
    mutationFn: (id: number): Promise<ScanResponse> =>
      api("/scan", {
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
    mutationFn: (id: number): Promise<LibraryMountCheckResponse> =>
      v2("POST /api/v2/libraries/{id}/check-mount", { path: { id: String(id) } }).then(
        mountCheckFromV2,
      ),
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
    mutationFn: (): Promise<{ status: string }> =>
      api("/admin/tasks/scan_libraries/run", {
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
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number): Promise<{ cancelled: number; library_id: number }> =>
      api("/scan/cancel", {
        method: "POST",
        body: JSON.stringify({ library_id: id }),
      }),
    onSuccess: () => {
      toast.success("Scan cancellation requested");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel scans");
    },
  });
}

export function useLibraryMetadataMatchQueues() {
  const pageActivity = usePageActivity();

  return useQuery({
    queryKey: adminKeys.libraryMatchQueueStatuses(),
    queryFn: ({ signal }): Promise<LibraryMetadataMatchQueueStatus[]> =>
      v2("GET /api/v2/libraries/metadata-match-queue", { signal }).then((page) =>
        page.items.map(metadataMatchQueueStatusFromV2),
      ),
    staleTime: 0,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? 10_000 : false,
  });
}

const METADATA_MATCH_QUEUE_PAGE_SIZE = 10;

type MetadataMatchQueueDetailV2 = V2Result<"GET /api/v2/libraries/{id}/metadata-match-queue">;

/**
 * One page of a library's matcher backlog as the admin screen renders it.
 * The v2 listing is cursor paginated; the screen keeps a numeric offset for
 * its "Page N" label, and the hook resolves that offset by walking cursors
 * from the first page (the backlog view is ten rows per page, so the walk is
 * short in practice).
 */
export interface LibraryMetadataMatchQueuePage extends Omit<
  MetadataMatchQueueDetailV2,
  "library_id" | "page" | "movies" | "series" | "raw_files"
> {
  library_id: number;
  limit: number;
  offset: number;
  /** The failure detail with the fields the screen reads narrowed. */
  movies: Array<
    Omit<MetadataMatchQueueDetailV2["movies"][number], "failure_detail" | "library_id"> & {
      library_id: number;
      failure_detail?: LibraryMetadataMatchFailureDetail;
    }
  >;
  series: Array<
    Omit<MetadataMatchQueueDetailV2["series"][number], "failure_detail" | "library_id"> & {
      library_id: number;
      failure_detail?: LibraryMetadataMatchFailureDetail;
    }
  >;
  raw_files: Array<
    Omit<MetadataMatchQueueDetailV2["raw_files"][number], "library_id"> & { library_id: number }
  >;
  has_more: boolean;
}

function failureDetailFromV2(detail: unknown): LibraryMetadataMatchFailureDetail | undefined {
  if (typeof detail !== "object" || detail === null || Array.isArray(detail)) return undefined;
  return detail as LibraryMetadataMatchFailureDetail;
}

export function metadataMatchQueuePageFromV2(
  detail: MetadataMatchQueueDetailV2,
  offset: number,
): LibraryMetadataMatchQueuePage {
  const { page, ...rest } = detail;
  return {
    ...rest,
    library_id: Number(detail.library_id),
    limit: METADATA_MATCH_QUEUE_PAGE_SIZE,
    offset,
    movies: detail.movies.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
      failure_detail: failureDetailFromV2(entry.failure_detail),
    })),
    series: detail.series.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
      failure_detail: failureDetailFromV2(entry.failure_detail),
    })),
    raw_files: detail.raw_files.map((entry) => ({
      ...entry,
      library_id: Number(entry.library_id),
    })),
    has_more: page.has_more,
  };
}

export async function fetchLibraryMetadataMatchQueuePage(
  libraryId: number,
  offset: number,
  signal?: AbortSignal,
): Promise<LibraryMetadataMatchQueuePage> {
  let cursor: string | undefined;
  let skipped = 0;
  for (;;) {
    const detail = await v2("GET /api/v2/libraries/{id}/metadata-match-queue", {
      path: { id: String(libraryId) },
      query: {
        limit: METADATA_MATCH_QUEUE_PAGE_SIZE,
        ...(cursor === undefined ? {} : { cursor }),
      },
      signal,
    });
    if (
      skipped >= offset ||
      !detail.page.has_more ||
      !detail.page.next_cursor ||
      detail.page.next_cursor === cursor
    ) {
      return metadataMatchQueuePageFromV2(detail, skipped);
    }
    skipped += METADATA_MATCH_QUEUE_PAGE_SIZE;
    cursor = detail.page.next_cursor;
  }
}

export function useLibraryMetadataMatchQueueDetail(libraryId: number | null, offset = 0) {
  const pageActivity = usePageActivity();

  return useQuery({
    queryKey: [...adminKeys.libraryMatchQueueDetail(libraryId ?? 0), offset],
    queryFn: ({ signal }) => fetchLibraryMetadataMatchQueuePage(libraryId ?? 0, offset, signal),
    enabled: libraryId !== null,
    staleTime: 0,
    refetchInterval: pageActivity.canApplyRealtimeUpdates ? 10_000 : false,
  });
}

export function useRetryLibraryMetadataMatchQueue() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      v2("POST /api/v2/libraries/{id}/metadata-match-queue/retry", {
        path: { id: String(id) },
      }),
    onSuccess: (_data, id) => {
      toast.success("Metadata matcher backlog queued");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueDetail(id) });
    },
    onError: (err) => {
      toast.error(
        err instanceof Error ? err.message : "Failed to rebuild metadata matcher backlog",
      );
    },
  });
}

export function useCancelLibraryMetadataMatchQueue() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      v2("POST /api/v2/libraries/{id}/metadata-match-queue/cancel", {
        path: { id: String(id) },
      }),
    onSuccess: (_data, id) => {
      toast.success("Metadata matcher backlog cancelled");
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueStatuses() });
      queryClient.invalidateQueries({ queryKey: adminKeys.libraryMatchQueueDetail(id) });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel metadata matcher backlog");
    },
  });
}

export function useLibraryProviders(libraryId: number | null) {
  return useQuery({
    queryKey: adminKeys.libraryProviders(libraryId ?? 0),
    queryFn: ({ signal }): Promise<LibraryProviderChainResponse> =>
      v2("GET /api/v2/libraries/{id}/providers", {
        path: { id: String(libraryId) },
        signal,
      }).then((d) => providerChainFromV2(d.levels)),
    enabled: libraryId !== null,
    staleTime: ADMIN_STALE_TIME,
  });
}

// useLibraryProviderDefaults fetches the provider chain the server would seed
// for a new library of the given type — the single source of truth the create
// form renders instead of re-deriving defaults from plugin manifests.
export function useLibraryProviderDefaults(libraryType: string) {
  return useQuery({
    queryKey: adminKeys.libraryProviderDefaults(libraryType),
    queryFn: ({ signal }): Promise<LibraryProviderChainResponse> =>
      v2("GET /api/v2/libraries/provider-defaults", {
        query: { library_type: libraryType },
        signal,
      }).then((d) => providerChainFromV2(d.levels)),
    enabled: libraryType !== "",
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useSetLibraryProviders() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: SetLibraryChainRequest }) =>
      v2("PUT /api/v2/libraries/{id}/providers", {
        path: { id: String(id) },
        body: providerChainToV2(body.levels),
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
    mutationFn: async ({ id, file }: { id: number; file: File }): Promise<Library> => {
      const library = await v2("PUT /api/v2/libraries/{id}/poster", {
        path: { id: String(id) },
        form: { poster: file },
      });
      return libraryFromV2(library);
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
    mutationFn: (id: number) =>
      v2("DELETE /api/v2/libraries/{id}/poster", { path: { id: String(id) } }),
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
    mutationFn: (id: number): Promise<AdminJob> =>
      v2("POST /api/v2/libraries/{id}/refresh-metadata", {
        path: { id: String(id) },
      }).then(adminJobFromV2),
    onSuccess: () => {
      toast.success("Metadata refresh queued");
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("library_refresh") });
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
    },
    onError: (err) => {
      // A 409 means a refresh for this library is already queued or running;
      // refetch the job lists so the active job shows up.
      if (err instanceof V2ProblemError && err.status === 409) {
        toast.error(err.message);
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("library_refresh") });
        queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
        return;
      }
      toast.error(err instanceof Error ? err.message : "Refresh failed");
    },
  });
}

export function useCancelAdminJob() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: string): Promise<AdminJob> =>
      api(`/admin/jobs/${encodeURIComponent(id)}/cancel`, { method: "POST" }),
    onSuccess: () => {
      toast.success("Cancellation requested");
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("library_refresh") });
      queryClient.invalidateQueries({ queryKey: adminKeys.jobs("__all") });
    },
    onError: (err) => {
      toast.error(err instanceof Error ? err.message : "Failed to cancel job");
    },
  });
}

const UNMATCHED_PAGE_SIZE = 10;

export interface UnmatchedLibraryItemsPage {
  items: UnmatchedLibraryItem[];
  total: number;
}

/**
 * Fetches page `page` of the unmatched-item listing. v2 is cursor paginated
 * and the admin table keeps a page number for its range label and total, so
 * the hook walks cursors from the first page to reach the requested one.
 */
export async function fetchUnmatchedLibraryItemsPage(
  page: number,
  search: string,
  signal?: AbortSignal,
): Promise<UnmatchedLibraryItemsPage> {
  let cursor: string | undefined;
  let index = 0;
  for (;;) {
    const result = await v2("GET /api/v2/libraries/unmatched-items", {
      query: {
        limit: UNMATCHED_PAGE_SIZE,
        ...(search ? { q: search } : {}),
        ...(cursor === undefined ? {} : { cursor }),
      },
      signal,
    });
    if (
      index >= page ||
      !result.page?.has_more ||
      !result.page.next_cursor ||
      result.page.next_cursor === cursor
    ) {
      return { items: result.items.map(unmatchedItemFromV2), total: result.total };
    }
    index += 1;
    cursor = result.page.next_cursor;
  }
}

export function useUnmatchedLibraryItems(page = 0, search = "") {
  const trimmed = search.trim();
  return useQuery({
    queryKey: adminKeys.unmatchedItems(page, trimmed),
    queryFn: ({ signal }) => fetchUnmatchedLibraryItemsPage(page, trimmed, signal),
    staleTime: ADMIN_STALE_TIME,
    placeholderData: (prev) => prev,
  });
}

export { UNMATCHED_PAGE_SIZE };

export function useConfirmEmptyRootCleanup() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) =>
      v2("POST /api/v2/libraries/{id}/confirm-empty-root-cleanup", {
        path: { id: String(id) },
      }),
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
    queryFn: async () => {
      const data: AdminJobsResponse = await api(
        `/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=10`,
      );
      return data.jobs ?? [];
    },
    staleTime: 0,
  });
}

export function useCatalogImportJobs(jobType = "catalog_import") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: async () => {
      const data: AdminJobsResponse = await api(
        `/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=10`,
      );
      return data.jobs ?? [];
    },
    staleTime: 0,
  });
}

export function useLibraryDeleteJobs(jobType = "delete_library") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: async () => {
      const data: AdminJobsResponse = await api(
        `/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=20`,
      );
      return data.jobs ?? [];
    },
    staleTime: 0,
  });
}

export function useLibraryRefreshJobs(jobType = "library_refresh") {
  return useQuery({
    queryKey: adminKeys.jobs(jobType),
    queryFn: async () => {
      const data: AdminJobsResponse = await api(
        `/admin/jobs?job_type=${encodeURIComponent(jobType)}&limit=50`,
      );
      return data.jobs ?? [];
    },
    staleTime: 0,
  });
}

export function useAllAdminJobs(limit = 30) {
  return useQuery({
    queryKey: adminKeys.jobs("__all"),
    queryFn: async () => {
      const data: AdminJobsResponse = await api(`/admin/jobs?limit=${limit}`);
      return data.jobs ?? [];
    },
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

export function fetchFilesystemBrowse(path: string): Promise<FilesystemBrowseResponse> {
  return api(`/admin/filesystem/browse?path=${encodeURIComponent(path)}`);
}
