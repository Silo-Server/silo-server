import type { PageSectionConfig } from "@/api/types";
import type { components } from "@/api/v2/schema";
import { v2, V2ProblemError, type V2Body } from "@/api/v2/request";

export function adminSectionFromV2(
  value: components["schemas"]["AdminSection"],
): PageSectionConfig {
  return { ...value, library_id: value.library_id === null ? null : Number(value.library_id) };
}
function scopeQuery(scope: string, libraryId?: number) {
  if (scope !== "home" && scope !== "library") throw new Error("Select a valid section scope.");
  return { scope, library_id: libraryId === undefined ? undefined : String(libraryId) } as const;
}
function requiredSectionETag(etag: string | null | undefined): string {
  if (!etag || etag === "*")
    throw new Error("Reload these sections before editing; their version is unavailable.");
  return etag;
}
export function adminSectionMutationMessage(error: unknown, fallback: string) {
  return error instanceof V2ProblemError && error.status === 412
    ? "These sections changed. Reload and review your changes before trying again."
    : error instanceof Error
      ? error.message
      : fallback;
}
export async function fetchAdminSectionSnapshot(id: string, signal?: AbortSignal) {
  let etag: string | null = null;
  const section = await v2("GET /api/v2/admin/sections/{id}", {
    path: { id },
    signal,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { section: adminSectionFromV2(section), etag: requiredSectionETag(etag) };
}
export async function fetchAdminSectionOrderSnapshot(
  scope: string,
  libraryId?: number,
  signal?: AbortSignal,
) {
  let etag: string | null = null;
  const order = await v2("GET /api/v2/admin/sections/order", {
    query: scopeQuery(scope, libraryId),
    signal,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { ...order, etag: requiredSectionETag(etag) };
}
/** The two scope witnesses fence both definitions and full membership, including disabled rows. */
export async function fetchAdminSections(scope: string, libraryId?: number, signal?: AbortSignal) {
  const before = await fetchAdminSectionOrderSnapshot(scope, libraryId, signal);
  const list = await v2("GET /api/v2/admin/sections", {
    query: scopeQuery(scope, libraryId),
    signal,
  });
  const after = await fetchAdminSectionOrderSnapshot(scope, libraryId, signal);
  const byID = new Map(list.items.map((section) => [section.id, section]));
  if (
    before.etag !== after.etag ||
    byID.size !== after.ordered_ids.length ||
    after.ordered_ids.some((id) => !byID.has(id)) ||
    byID.size !== list.items.length
  ) {
    throw new Error("Sections changed while loading. Reload before editing.");
  }
  return {
    sections: after.ordered_ids.map((id) => adminSectionFromV2(byID.get(id)!)),
    ordered_ids: after.ordered_ids,
    etag: after.etag,
  };
}
export type AdminSectionDeleteTarget = { id: string; etag: string | null };
/** Capture before confirmation; absent targets are already deleted, never silently reloaded on submit. */
export async function fetchAdminSectionDeleteTargets(
  ids: string[],
): Promise<AdminSectionDeleteTarget[]> {
  const unique = [...new Set(ids)];
  const targets: AdminSectionDeleteTarget[] = [];
  for (let start = 0; start < unique.length; start += 4) {
    targets.push(
      ...(await Promise.all(
        unique.slice(start, start + 4).map(async (id) => {
          try {
            return { id, etag: (await fetchAdminSectionSnapshot(id)).etag };
          } catch (error) {
            if (error instanceof V2ProblemError && error.status === 404) return { id, etag: null };
            throw error;
          }
        }),
      )),
    );
  }
  return targets;
}
export function fetchAdminSectionCapabilities() {
  return v2("GET /api/v2/admin/sections/capabilities");
}
export async function createAdminSection(data: Partial<PageSectionConfig>) {
  if (!data.section_type || !data.title) throw new Error("A section type and title are required.");
  const body: V2Body<"POST /api/v2/admin/sections"> = {
    ...scopeQuery(data.scope ?? "home", data.library_id ?? undefined),
    section_type: data.section_type,
    title: data.title,
    position: data.position,
    featured: data.featured,
    item_limit: data.item_limit,
    config: data.config,
    enabled: data.enabled,
  };
  return adminSectionFromV2(await v2("POST /api/v2/admin/sections", { body }));
}
export type BulkCreateAdminSections = Omit<
  V2Body<"POST /api/v2/admin/sections/bulk">,
  "library_ids"
> & { library_ids?: number[] };
export function bulkCreateAdminSections(data: BulkCreateAdminSections) {
  if ((data.library_ids?.length ?? 0) > 100)
    throw new Error("Select at most 100 libraries per request.");
  return v2("POST /api/v2/admin/sections/bulk", {
    body: { ...data, library_ids: data.library_ids?.map(String) },
  });
}
export async function updateAdminSection({
  id,
  etag,
  ...data
}: Partial<PageSectionConfig> & { id: string; etag: string }) {
  const { title, section_type, position, featured, item_limit, config, enabled } = data;
  const section = await v2("PATCH /api/v2/admin/sections/{id}", {
    path: { id },
    headers: { "If-Match": requiredSectionETag(etag) },
    body: { title, section_type, position, featured, item_limit, config, enabled },
  });
  return adminSectionFromV2(section);
}
export function deleteAdminSection({ id, etag }: AdminSectionDeleteTarget) {
  if (etag === null) return Promise.resolve();
  return v2("DELETE /api/v2/admin/sections/{id}", {
    path: { id },
    headers: { "If-Match": requiredSectionETag(etag) },
  });
}
export interface AdminSectionScopeMutation {
  scope: string;
  library_id?: number;
  etag: string;
}
export function reorderAdminSections({
  scope,
  library_id,
  etag,
  ordered_ids,
}: AdminSectionScopeMutation & { ordered_ids: string[] }) {
  return v2("PUT /api/v2/admin/sections/order", {
    query: scopeQuery(scope, library_id),
    headers: { "If-Match": requiredSectionETag(etag) },
    body: { ordered_ids },
  });
}
export function restoreAdminSections({
  scope,
  library_id,
  etag,
  reset_profiles,
}: AdminSectionScopeMutation & { reset_profiles?: boolean }) {
  return v2("PUT /api/v2/admin/sections/defaults", {
    query: scopeQuery(scope, library_id),
    headers: { "If-Match": requiredSectionETag(etag) },
    body: { reset_profiles },
  });
}
