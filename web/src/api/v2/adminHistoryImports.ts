import { type ProfileRequestContextSnapshot } from "@/api/client";
import type {
  HistoryImportSource,
  HistoryImportUserMapping,
  HistoryImportRun,
  CreateHistoryImportSourceRequest,
  UpdateHistoryImportSourceRequest,
  CreateHistoryImportMappingRequest,
  UpdateHistoryImportMappingRequest,
} from "@/api/types";
import { v2, V2ProblemError } from "./request";
import type { components } from "./schema";
import { historyImportRunFromV2 } from "@/hooks/queries/history-import";
import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";

type SourceWire = components["schemas"]["AdminHistoryImportSource"];
type MappingWire = components["schemas"]["AdminHistoryImportMapping"];
export type AdminImportRun = Omit<HistoryImportRun, "status"> & {
  status: HistoryImportRun["status"] | "canceling";
  terminal: boolean;
  cancelable: boolean;
  location?: string;
  retryAfterMs?: number;
};
export const adminImportScope = adminAuthorityScope;
export const importAuthority = captureAdminAuthority;
const checkAuthority = requireAdminAuthority;
function tag(etag?: string) {
  if (!etag) throw new Error("Reload this configuration before saving.");
  return etag;
}
export const isAdminImportConflict = (error: unknown) =>
  error instanceof V2ProblemError && error.status === 412;
function headers() {
  let etag = "";
  let location: string | undefined;
  let retryAfterMs = 5000;
  return {
    onResponse: (r: Response) => {
      etag = r.headers.get("ETag") ?? "";
      location = r.headers.get("Location") ?? undefined;
      const seconds = Number(r.headers.get("Retry-After"));
      if (Number.isFinite(seconds) && seconds > 0) retryAfterMs = seconds * 1000;
    },
    etag: () => tag(etag),
    run: () => ({ location, retryAfterMs }),
  };
}
function sourceOf(s: SourceWire, etag: string): HistoryImportSource {
  return { ...s, id: Number(s.id), etag, created_at: "", updated_at: "" };
}
function mappingOf(m: MappingWire, etag: string): HistoryImportUserMapping {
  return {
    ...m,
    id: Number(m.id),
    source_id: Number(m.source_id),
    silo_user_id: Number(m.silo_user_id),
    etag,
    created_at: "",
    updated_at: "",
  };
}
async function walk<T>(
  fetchPage: (
    cursor: string | undefined,
  ) => Promise<{ items: T[]; page?: { has_more: boolean; next_cursor?: string } }>,
  c: ProfileRequestContextSnapshot,
  max = Infinity,
) {
  const rows: T[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  do {
    checkAuthority(c);
    const page = await fetchPage(cursor);
    rows.push(...page.items);
    if (!page.page?.has_more) break;
    const next = page.page.next_cursor;
    if (!next || seen.has(next))
      throw new Error("Incomplete history import page. Reload to try again.");
    seen.add(next);
    cursor = next;
  } while (rows.length < max);
  checkAuthority(c);
  return rows.slice(0, max);
}
export async function getAdminImportSource(id: number, profileContext = importAuthority()) {
  const h = headers();
  const body = await v2("GET /api/v2/admin/history-import-sources/{id}", {
    path: { id: String(id) },
    profileContext,
    onResponse: h.onResponse,
  });
  return sourceOf(body, h.etag());
}
export async function listAdminImportSources() {
  const profileContext = importAuthority();
  const rows = await walk(
    (cursor) =>
      v2("GET /api/v2/admin/history-import-sources", {
        query: { limit: 200, cursor },
        profileContext,
      }),
    profileContext,
  );
  const sources = await Promise.all(
    rows.map((row) => getAdminImportSource(Number(row.id), profileContext)),
  );
  checkAuthority(profileContext);
  return sources;
}
export async function createAdminImportSource(body: CreateHistoryImportSourceRequest) {
  const h = headers();
  const result = await v2("POST /api/v2/admin/history-import-sources", {
    body,
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return sourceOf(result, h.etag());
}
export async function updateAdminImportSource(
  id: number,
  body: UpdateHistoryImportSourceRequest,
  etag?: string,
) {
  const h = headers();
  const result = await v2("PUT /api/v2/admin/history-import-sources/{id}", {
    path: { id: String(id) },
    body,
    headers: { "If-Match": tag(etag) },
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return sourceOf(result, h.etag());
}
export function deleteAdminImportSource(id: number, etag?: string) {
  return v2("DELETE /api/v2/admin/history-import-sources/{id}", {
    path: { id: String(id) },
    headers: { "If-Match": tag(etag) },
    profileContext: importAuthority(),
  });
}
export async function setAdminImportToken(id: number, token: string, etag?: string) {
  const h = headers();
  const result = await v2("PUT /api/v2/admin/history-imports/sources/{id}/token", {
    path: { id: String(id) },
    body: { token },
    headers: { "If-Match": tag(etag) },
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return sourceOf(result, h.etag());
}
export async function clearAdminImportToken(id: number, etag?: string) {
  const profileContext = importAuthority();
  await v2("DELETE /api/v2/admin/history-imports/sources/{id}/token", {
    path: { id: String(id) },
    headers: { "If-Match": tag(etag) },
    profileContext,
  });
  return getAdminImportSource(id, profileContext);
}
export async function getAdminImportMapping(id: number, profileContext = importAuthority()) {
  const h = headers();
  const body = await v2("GET /api/v2/admin/history-imports/mappings/{id}", {
    path: { id: String(id) },
    profileContext,
    onResponse: h.onResponse,
  });
  return mappingOf(body, h.etag());
}
export async function listAdminImportMappings(sourceId: number) {
  const profileContext = importAuthority();
  const rows = await walk(
    (cursor) =>
      v2("GET /api/v2/admin/history-imports/mappings", {
        query: { source_id: String(sourceId), limit: 200, cursor },
        profileContext,
      }),
    profileContext,
  );
  const mappings = await Promise.all(
    rows.map((row) => getAdminImportMapping(Number(row.id), profileContext)),
  );
  checkAuthority(profileContext);
  return mappings;
}
export async function createAdminImportMapping(input: CreateHistoryImportMappingRequest) {
  const h = headers();
  const body = {
    ...input,
    source_id: String(input.source_id),
    silo_user_id: String(input.silo_user_id),
  };
  const result = await v2("POST /api/v2/admin/history-imports/mappings", {
    body,
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return mappingOf(result, h.etag());
}
export async function updateAdminImportMapping(
  id: number,
  input: UpdateHistoryImportMappingRequest,
  etag?: string,
) {
  const h = headers();
  const body = {
    ...input,
    silo_user_id: input.silo_user_id == null ? undefined : String(input.silo_user_id),
  };
  const result = await v2("PUT /api/v2/admin/history-imports/mappings/{id}", {
    path: { id: String(id) },
    body,
    headers: { "If-Match": tag(etag) },
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return mappingOf(result, h.etag());
}
export function deleteAdminImportMapping(id: number, etag?: string) {
  return v2("DELETE /api/v2/admin/history-imports/mappings/{id}", {
    path: { id: String(id) },
    headers: { "If-Match": tag(etag) },
    profileContext: importAuthority(),
  });
}
export function discoverAdminImportUsers(id: number) {
  const profileContext = importAuthority();
  return walk(
    (cursor) =>
      v2("GET /api/v2/admin/history-imports/sources/{id}/users", {
        path: { id: String(id) },
        query: { limit: 200, cursor },
        profileContext,
      }),
    profileContext,
  );
}
export function plexAdminImportLogin(body: { username: string; password: string }) {
  return v2("POST /api/v2/admin/history-imports/plex/login", {
    body,
    profileContext: importAuthority(),
  });
}
function adminRunOf(run: components["schemas"]["AdminHistoryImportRun"]): AdminImportRun {
  return {
    ...historyImportRunFromV2(run),
    status: run.status as AdminImportRun["status"],
    terminal: run.terminal,
    cancelable: run.cancelable,
  };
}
export const importRunActive = (run: Pick<AdminImportRun, "terminal">) => !run.terminal;
export async function getAdminImportRun(
  id: string,
  location?: string,
  profileContext = importAuthority(),
): Promise<AdminImportRun> {
  if (location && location !== `/api/v2/admin/history-imports/runs/${encodeURIComponent(id)}`)
    throw new Error("Unexpected import status location.");
  const h = headers();
  const body = await v2("GET /api/v2/admin/history-imports/runs/{id}", {
    path: { id },
    profileContext,
    onResponse: h.onResponse,
  });
  return {
    ...adminRunOf(body),
    ...h.run(),
    location: location ?? `/api/v2/admin/history-imports/runs/${encodeURIComponent(id)}`,
  };
}
export async function createAdminImportRun(id: number): Promise<AdminImportRun> {
  const h = headers();
  const body = await v2("POST /api/v2/admin/history-imports/mappings/{id}/run", {
    path: { id: String(id) },
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  const hint = h.run();
  if (hint.location !== `/api/v2/admin/history-imports/runs/${encodeURIComponent(body.id)}`)
    throw new Error(
      "The accepted import did not include its expected status location. Refresh runs before trying again.",
    );
  return { ...adminRunOf(body), ...hint };
}
export async function getAdminImportRunsPage(
  sourceId?: number,
  cursor?: string,
  profileContext = importAuthority(),
) {
  checkAuthority(profileContext);
  const result = await v2("GET /api/v2/admin/history-imports/runs", {
    query: { source_id: sourceId == null ? undefined : String(sourceId), limit: 50, cursor },
    profileContext,
  });
  if (result.page?.has_more && (!result.page.next_cursor || result.page.next_cursor === cursor))
    throw new Error("Incomplete history import page. Reload to try again.");
  checkAuthority(profileContext);
  return {
    items: result.items.map(adminRunOf),
    nextCursor: result.page?.has_more ? result.page.next_cursor : undefined,
  };
}
export async function cancelAdminImportRun(id: string) {
  const h = headers();
  const body = await v2("POST /api/v2/admin/history-imports/runs/{id}/cancel", {
    path: { id },
    profileContext: importAuthority(),
    onResponse: h.onResponse,
  });
  return { ...adminRunOf(body), ...h.run() };
}
export async function bulkAdminImportRuns(id: number) {
  const result = await v2("POST /api/v2/admin/history-imports/sources/{id}/bulk-run", {
    path: { id: String(id) },
    profileContext: importAuthority(),
  });
  return {
    ...result,
    outcomes: result.outcomes.map((outcome) => ({
      ...outcome,
      mapping_id: Number(outcome.mapping_id),
      run: outcome.run ? { ...adminRunOf(outcome.run), location: outcome.location } : undefined,
    })),
  };
}
export const adminImportCapabilities = () =>
  v2("GET /api/v2/admin/history-imports/capabilities", { profileContext: importAuthority() });
