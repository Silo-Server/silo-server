import { useState } from "react";
import { queryOptions, useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import { V2ProblemError } from "@/api/v2/request";
import {
  fetchAdminSections,
  fetchAdminSectionCapabilities,
  deleteAdminSection,
  restoreAdminSections,
  type AdminSectionDeleteTarget,
} from "@/api/adminSections";
import { sectionFromV2 } from "@/api/v2/catalog";
import type {
  HomeLayoutResponse,
  LibraryLayoutResponse,
  HomeSectionItemsResponse,
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
  /** The profile to write as, when it must not follow a later profile switch. */
  profileContext?: ProfileRequestContextSnapshot;
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

export function useHomeLayout(enabled = true) {
  return useQuery({
    queryKey: sectionKeys.homeLayout(),
    queryFn: ({ signal }): Promise<HomeLayoutResponse> =>
      v2("GET /api/v2/home/layout", { signal }).then((layout) => ({ sections: layout.sections })),
    staleTime: HOME_SECTION_STALE_TIME,
    gcTime: HOME_SECTION_GC_TIME,
    enabled,
  });
}

/**
 * Fetches one home section with its items. Answers in the same `{ section }`
 * shape as the library section fetch so the section caches and refresh
 * helpers treat both alike.
 */
export function fetchHomeSectionItems(
  sectionId: string,
  options?: { signal?: AbortSignal },
): Promise<HomeSectionItemsResponse> {
  return v2("GET /api/v2/home/sections/{id}/items", {
    path: { id: sectionId },
    signal: options?.signal,
  }).then((section) => ({ section: sectionFromV2(section) }));
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

/** One page's admin rows, as the Home rows pages read them. */
export function adminSectionsQuery(scope: string, libraryId?: number) {
  return queryOptions({
    queryKey: sectionKeys.adminList(scope, libraryId),
    queryFn: ({ signal }) => fetchAdminSections(scope, libraryId, signal),
    enabled: scope !== "library" || Boolean(libraryId),
  });
}

export function useAdminSections(scope: string, libraryId?: number, enabled = true) {
  const query = adminSectionsQuery(scope, libraryId);
  return useQuery({ ...query, enabled: enabled && query.enabled });
}

export function useAdminSectionCapabilities() {
  return useQuery({
    queryKey: [...sectionKeys.all, "admin-capabilities"],
    queryFn: fetchAdminSectionCapabilities,
  });
}

export function useDeleteSection() {
  const qc = useQueryClient();
  return useMutation({
    retry: false,
    mutationKey: sectionKeys.adminWrite(),
    mutationFn: deleteAdminSection,
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
    retry: false,
    mutationKey: sectionKeys.adminWrite(),
    onMutate: (targets) => {
      setProgress({ completed: 0, total: new Set(targets.map((target) => target.id)).size });
    },
    mutationFn: async (targets: AdminSectionDeleteTarget[]) => {
      const byID = new Map(targets.map((target) => [target.id, target]));
      const deletedIds: string[] = [];
      const failedIds: string[] = [];
      const result = await runBulkDelete(
        [...byID.keys()],
        async (id) => {
          try {
            await deleteAdminSection(byID.get(id)!);
            deletedIds.push(id);
          } catch (error) {
            if (error instanceof V2ProblemError && error.status === 404) deletedIds.push(id);
            else failedIds.push(id);
            throw error;
          }
        },
        (error) => (error instanceof V2ProblemError && error.status === 404 ? "deleted" : "failed"),
        setProgress,
      );
      return { ...result, deletedIds, failedIds };
    },
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

function sectionScopeQuery(scope: ProfileSectionScope, libraryId?: string | number) {
  return { scope, library_id: libraryId ? String(libraryId) : undefined };
}

/** The cache key of one page's rows as this profile sees them, in order. */
export function profileSectionSettingsKey(scope: ProfileSectionScope, libraryId?: number) {
  return sectionKeys.profileOverrides(scope, libraryId ? String(libraryId) : undefined);
}

/** One page's rows as this profile sees them, in order, hidden ones included. */
export async function fetchProfileSectionSettings(
  scope: ProfileSectionScope,
  libraryId?: number,
  signal?: AbortSignal,
): Promise<{ sections: SettingsSectionEntry[] }> {
  const settings = await v2("GET /api/v2/profile/sections/settings", {
    query: sectionScopeQuery(scope, libraryId),
    signal,
  });
  return { sections: settings.items };
}

export function useProfileSectionSettings(scope: ProfileSectionScope, libraryId?: number) {
  return useQuery({
    queryKey: profileSectionSettingsKey(scope, libraryId),
    queryFn: () => fetchProfileSectionSettings(scope, libraryId),
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

/**
 * Replaces the profile's overrides for one page. Not retry-safe and unguarded
 * (last write wins), so callers serialize their own saves and refetch after.
 */
export function replaceProfileSectionOverrides(data: SaveOverridesRequest) {
  return v2("PUT /api/v2/profile/sections", {
    query: sectionScopeQuery(data.scope, data.library_id),
    body: { overrides: data.overrides },
    profileContext: data.profileContext,
  });
}

/** Drops every override the profile saved for one page. */
export function resetProfileSectionOverrides(params: {
  scope: ProfileSectionScope;
  libraryId?: string;
  profileContext?: ProfileRequestContextSnapshot;
}) {
  return v2("DELETE /api/v2/profile/sections", {
    query: sectionScopeQuery(params.scope, params.libraryId),
    profileContext: params.profileContext,
  });
}

export function useRestoreDefaultSections() {
  const qc = useQueryClient();
  return useMutation({
    retry: false,
    mutationKey: sectionKeys.adminWrite(),
    mutationFn: restoreAdminSections,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: sectionKeys.all });
    },
  });
}
