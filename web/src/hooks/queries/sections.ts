import { useState } from "react";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { ApiClientError, api } from "@/api/client";
import type {
  SectionsResponse,
  HomeLayoutResponse,
  LibraryLayoutResponse,
  HomeSectionItemsResponse,
  PageSectionListResponse,
  PageSectionConfig,
  SectionOverride,
  SettingsSectionEntry,
} from "@/api/types";
import type { components } from "@/api/v2/schema";
import { v2, type V2Query } from "@/api/v2/request";
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

/** The scope of a profile's section overrides, as the v2 contract enumerates it. */
export type ProfileSectionScope = NonNullable<V2Query<"GET /api/v2/profile/sections">["scope"]>;

export interface SaveOverridesRequest {
  scope: ProfileSectionScope;
  library_id?: string;
  overrides: SectionOverride[];
}

/**
 * Projects the stored v2 overrides onto the `SectionOverride` shape the home
 * screen editor reads and writes back: null members become absent ones.
 */
export function profileSectionOverridesFromV2(
  items: components["schemas"]["SectionOverride"][],
): SectionOverride[] {
  return items.map((override) => ({
    id: override.id || undefined,
    section_id: override.section_id || undefined,
    position: override.position ?? undefined,
    hidden: override.hidden,
    removed: override.removed,
    section_type: override.section_type || undefined,
    title: override.title || undefined,
    featured: override.featured ?? undefined,
    item_limit: override.item_limit ?? undefined,
    config: override.config,
  }));
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
    queryFn: () => api<HomeLayoutResponse>("/home/layout"),
    staleTime: HOME_SECTION_STALE_TIME,
    gcTime: HOME_SECTION_GC_TIME,
  });
}

export function fetchHomeSectionItems(sectionId: string, options?: RequestInit) {
  return api<HomeSectionItemsResponse>(`/home/sections/${sectionId}/items`, options);
}

export function fetchLibrarySectionItems(
  libraryId: number,
  sectionId: string,
  options?: RequestInit,
) {
  return api<HomeSectionItemsResponse>(
    `/library/${libraryId}/sections/${sectionId}/items`,
    options,
  );
}

export function useLibraryLayout(libraryId: number) {
  return useQuery({
    queryKey: sectionKeys.libraryLayout(libraryId),
    queryFn: () => api<LibraryLayoutResponse>(`/library/${libraryId}/layout`),
    staleTime: 5 * 60 * 1000,
    enabled: libraryId > 0,
  });
}

export function useLibrarySections(libraryId: number) {
  return useQuery({
    queryKey: sectionKeys.library(libraryId),
    queryFn: () => api<SectionsResponse>(`/library/${libraryId}/sections`),
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

function sectionScopeQuery(scope: ProfileSectionScope, libraryId?: string | number) {
  return { scope, library_id: libraryId ? String(libraryId) : undefined };
}

export function useProfileSectionSettings(scope: ProfileSectionScope, libraryId?: number) {
  return useQuery({
    queryKey: sectionKeys.profileOverrides(scope, libraryId ? String(libraryId) : undefined),
    queryFn: async (): Promise<{ sections: SettingsSectionEntry[] }> => {
      const settings = await v2("GET /api/v2/profile/sections/settings", {
        query: sectionScopeQuery(scope, libraryId),
      });
      return { sections: settings.items };
    },
  });
}

export function useProfileSectionOverrides(scope: ProfileSectionScope, libraryId?: number) {
  return useQuery({
    queryKey: sectionKeys.profileOverridesRaw(scope, libraryId ? String(libraryId) : undefined),
    queryFn: async (): Promise<{ overrides: SectionOverride[] }> => {
      const overrides = await v2("GET /api/v2/profile/sections", {
        query: sectionScopeQuery(scope, libraryId),
      });
      return { overrides: profileSectionOverridesFromV2(overrides.items) };
    },
  });
}

export function useSaveProfileOverrides() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (data: SaveOverridesRequest) =>
      v2("PUT /api/v2/profile/sections", {
        query: sectionScopeQuery(data.scope, data.library_id),
        body: { overrides: data.overrides },
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}

export function useResetProfileOverrides() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (params: { scope: ProfileSectionScope; libraryId?: string }) =>
      v2("DELETE /api/v2/profile/sections", {
        query: sectionScopeQuery(params.scope, params.libraryId),
      }),
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
