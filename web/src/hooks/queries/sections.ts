import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiClientError, api } from "@/api/client";
import { sectionFromV2 } from "@/api/v2/catalog";
import { v2 } from "@/api/v2/request";
import type {
  SectionsResponse,
  HomeLayoutResponse,
  LibraryLayoutResponse,
  HomeSectionItemsResponse,
  PageSectionListResponse,
  PageSectionConfig,
  SaveOverridesRequest,
  ProfileSectionOverridesResponse,
  SettingsSectionsResponse,
} from "@/api/types";
import { sectionKeys } from "./keys";
import { invalidateAdminCollectionQueries } from "./collectionSurfaceRefresh";
import { runBulkDelete, type BulkDeleteProgress } from "./bulkDelete";

/**
 * Home section data outlives the default client-wide gcTime on purpose.
 *
 * The layout query loses its observer the moment home unmounts, and the section
 * items are fetched observer-less through `queryClient.fetchQuery`, so the
 * 10 minute default evicts everything home needs mid-session and the next visit
 * repaints the whole skeleton grid. Keeping the entries for an hour makes a
 * return render from cache; invalidation still drives real refreshes.
 */
export const HOME_SECTION_STALE_TIME = 10 * 60 * 1000;
export const HOME_SECTION_GC_TIME = 60 * 60 * 1000;

interface RawSectionOverride {
  ID?: string;
  SectionID?: string;
  Position?: number | null;
  Hidden?: boolean;
  Removed?: boolean;
  SectionType?: string;
  Title?: string;
  Featured?: boolean | null;
  ItemLimit?: number | null;
  Config?: string;
}

interface RawProfileSectionOverridesResponse {
  overrides: RawSectionOverride[];
}

function parseOverrideConfig(config?: string): Record<string, unknown> | undefined {
  if (!config) {
    return undefined;
  }

  try {
    const parsed = JSON.parse(config) as unknown;
    return parsed && typeof parsed === "object" && !Array.isArray(parsed)
      ? (parsed as Record<string, unknown>)
      : undefined;
  } catch {
    return undefined;
  }
}

export function normalizeProfileSectionOverridesResponse(
  response: RawProfileSectionOverridesResponse,
): ProfileSectionOverridesResponse {
  return {
    overrides: response.overrides.map((override) => ({
      id: override.ID || undefined,
      section_id: override.SectionID || undefined,
      position: override.Position ?? undefined,
      hidden: override.Hidden,
      removed: override.Removed,
      section_type: override.SectionType || undefined,
      title: override.Title || undefined,
      featured: override.Featured ?? undefined,
      item_limit: override.ItemLimit ?? undefined,
      config: parseOverrideConfig(override.Config),
    })),
  };
}

export function useHomeSections(enabled = true) {
  return useQuery({
    queryKey: sectionKeys.home(),
    queryFn: () => api<SectionsResponse>("/home/sections"),
    staleTime: 5 * 60 * 1000,
    enabled,
  });
}

export function useHomeLayout() {
  return useQuery({
    queryKey: sectionKeys.homeLayout(),
    queryFn: ({ signal }): Promise<HomeLayoutResponse> =>
      v2("GET /api/v2/home/layout", { signal }).then((layout) => ({ sections: layout.sections })),
    staleTime: HOME_SECTION_STALE_TIME,
    gcTime: HOME_SECTION_GC_TIME,
  });
}

export function fetchHomeSectionItems(sectionId: string, options?: RequestInit) {
  return api<HomeSectionItemsResponse>(`/home/sections/${sectionId}/items`, options);
}

/**
 * Fetches one library section with its items. Answers in the same
 * `{ section }` shape as the home section fetch so the section caches and
 * refresh helpers treat both alike.
 */
export function fetchLibrarySectionItems(
  libraryId: number,
  sectionId: string,
  options?: { signal?: AbortSignal },
): Promise<HomeSectionItemsResponse> {
  return v2("GET /api/v2/library/{id}/sections/{section_id}/items", {
    path: { id: String(libraryId), section_id: sectionId },
    signal: options?.signal,
  }).then((section) => ({ section: sectionFromV2(section) }));
}

export function useLibraryLayout(libraryId: number) {
  return useQuery({
    queryKey: sectionKeys.libraryLayout(libraryId),
    queryFn: ({ signal }): Promise<LibraryLayoutResponse> =>
      v2("GET /api/v2/library/{id}/layout", { path: { id: String(libraryId) }, signal }).then(
        (layout) => ({ sections: layout.sections }),
      ),
    staleTime: 5 * 60 * 1000,
    enabled: libraryId > 0,
  });
}

export function useLibrarySections(libraryId: number) {
  return useQuery({
    queryKey: sectionKeys.library(libraryId),
    queryFn: ({ signal }): Promise<SectionsResponse> =>
      v2("GET /api/v2/library/{id}/sections", { path: { id: String(libraryId) }, signal }).then(
        (data) => ({ sections: data.sections.map(sectionFromV2) }),
      ),
    staleTime: 5 * 60 * 1000,
    enabled: libraryId > 0,
  });
}

export function useAdminSections(scope: string, libraryId?: number) {
  return useQuery({
    queryKey: sectionKeys.adminList(scope, libraryId),
    queryFn: () => {
      const params = new URLSearchParams({ scope });
      if (libraryId) params.set("library_id", String(libraryId));
      return api<PageSectionListResponse>(`/admin/sections?${params}`);
    },
    enabled: scope !== "library" || Boolean(libraryId),
  });
}

export function useCreateSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: Partial<PageSectionConfig>) =>
      api<PageSectionConfig>("/admin/sections", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export interface BulkCreateSectionsRequest {
  scope: "home" | "library";
  library_ids?: number[];
  section_type: string;
  title: string;
  featured: boolean;
  item_limit: number;
  config: Record<string, unknown>;
  enabled: boolean;
}

export interface BulkCreateSectionsResponse {
  created: number;
}

export function useBulkCreateSections() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: BulkCreateSectionsRequest) =>
      api<BulkCreateSectionsResponse>("/admin/sections/bulk-create", {
        method: "POST",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useUpdateSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: ({ id, ...data }: Partial<PageSectionConfig> & { id: string }) =>
      api<PageSectionConfig>(`/admin/sections/${id}`, {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useDeleteSection() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) => api<void>(`/admin/sections/${id}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
      void invalidateAdminCollectionQueries(qc);
    },
  });
}

export function useDeleteSections() {
  const qc = useQueryClient();
  const [progress, setProgress] = useState<BulkDeleteProgress | null>(null);

  const mutation = useMutation({
    onMutate: (ids) => {
      setProgress({ completed: 0, total: new Set(ids).size });
    },
    mutationFn: (ids: string[]) =>
      runBulkDelete(
        ids,
        (id) =>
          api<void>(`/admin/sections/${encodeURIComponent(id)}`, {
            method: "DELETE",
          }),
        (error) => (error instanceof ApiClientError && error.status === 404 ? "deleted" : "failed"),
        setProgress,
      ),
    onSuccess: async ({ requested, deleted, failed, firstError }) => {
      if (failed === 0) {
        toast.success(`Deleted ${deleted} section${deleted === 1 ? "" : "s"}`);
      } else if (deleted > 0) {
        toast.warning(`Deleted ${deleted} of ${requested} sections`, {
          description: firstError,
        });
      } else {
        toast.error(`Failed to delete ${failed} section${failed === 1 ? "" : "s"}`, {
          description: firstError,
        });
      }
      await Promise.all([
        qc.invalidateQueries({ queryKey: sectionKeys.all }),
        invalidateAdminCollectionQueries(qc),
      ]);
    },
    onSettled: () => {
      setProgress(null);
    },
  });

  return { ...mutation, progress };
}

export function useReorderSections() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (entries: Array<{ id: string; position: number }>) =>
      api<void>("/admin/sections/reorder", {
        method: "PUT",
        body: JSON.stringify({ entries }),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useProfileSectionSettings(scope: string, libraryId?: number) {
  return useQuery({
    queryKey: sectionKeys.profileOverrides(scope, libraryId ? String(libraryId) : undefined),
    queryFn: () => {
      const params = new URLSearchParams({ scope });
      if (libraryId) params.set("library_id", String(libraryId));
      return api<SettingsSectionsResponse>(`/profile/sections/settings?${params}`);
    },
  });
}

export function useProfileSectionOverrides(scope: string, libraryId?: number) {
  return useQuery({
    queryKey: sectionKeys.profileOverridesRaw(scope, libraryId ? String(libraryId) : undefined),
    queryFn: () => {
      const params = new URLSearchParams({ scope });
      if (libraryId) params.set("library_id", String(libraryId));
      return api<RawProfileSectionOverridesResponse>(`/profile/sections?${params}`).then(
        normalizeProfileSectionOverridesResponse,
      );
    },
  });
}

export function useSaveProfileOverrides() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: SaveOverridesRequest) =>
      api<void>("/profile/sections", {
        method: "PUT",
        body: JSON.stringify(data),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useResetProfileOverrides() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (params: { scope: string; libraryId?: string }) => {
      const searchParams = new URLSearchParams({ scope: params.scope });
      if (params.libraryId) searchParams.set("library_id", params.libraryId);
      return api<void>(`/profile/sections/reset?${searchParams}`, {
        method: "DELETE",
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useRestoreDefaultSections() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (params: { scope: string; library_id?: number; reset_profiles?: boolean }) =>
      api<PageSectionListResponse>("/admin/sections/restore-defaults", {
        method: "POST",
        body: JSON.stringify(params),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}
