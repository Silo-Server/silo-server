import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { api } from "@/api/client";
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
    queryFn: () => api<HomeLayoutResponse>("/home/layout"),
    staleTime: 5 * 60 * 1000,
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
