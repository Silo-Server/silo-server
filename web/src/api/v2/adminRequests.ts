import {
  captureProfileRequestContext,
  isProfileRequestContextCurrent,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  LoadRequestIntegrationOptionsRequest,
  MediaRequest,
  RequestIntegration,
  RequestMediaType,
  RequestSettings,
  RequestUserLimit,
} from "@/api/types";
import { v2, type V2Body, type V2Query, V2ProblemError } from "./request";
import { mediaRequestFromV2 } from "./requests";
import type { components } from "./schema";

type Schemas = components["schemas"];

export type RequestRouteMediaType = Schemas["AdminRequestRoute"]["media_type"];
export type RequestRouteConditions = Schemas["AdminRequestRouteConditions"];
export type RequestRouteDestination = Schemas["AdminRequestRouteDestination"];
/** The editable part of a routing rule; `media_type` is read on create only. */
export type RequestRouteBody = Schemas["AdminRequestRouteBody"];
export type RequestRoutePreview = Schemas["AdminRequestRoutePreviewOutputBody"];
export type RequestRoutePreviewTier = Schemas["AdminRequestRoutePreviewTier"];
export type RequestRoutePreviewRule = Schemas["AdminRequestRoutePreviewRule"];
export type RequestRouteFacts = Schemas["AdminRequestRouteFacts"];
export type RequestRouteTitle = Schemas["AdminRequestRouteTitle"];

/**
 * A routing rule, or a media type's fallback, with the validator of the read
 * it came from. A fallback that was never saved still has one: the server
 * answers it at revision zero, and its first save sends that tag.
 */
export type RequestRoute = Schemas["AdminRequestRoute"] & { etag: string };

function requireETag(etag?: string) {
  if (!etag) throw new Error("Reload this editor before saving.");
  return etag;
}
export const isRequestEditorConflict = (error: unknown) =>
  error instanceof V2ProblemError && error.status === 412;
export function requestValidationErrors(error: unknown) {
  if (!(error instanceof V2ProblemError) || error.problemType !== "validation_failed") return null;
  const fields: Record<string, string> = {};
  for (const field of error.problem.errors ?? []) {
    if (field.location?.startsWith("body.")) fields[field.location.slice(5)] = field.detail;
  }
  return { fields, message: error.message };
}
export async function getAdminRequestSettingsV2(): Promise<RequestSettings> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-settings", {
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, updated_at: "", etag: requireETag(etag) };
}
export async function putAdminRequestSettingsV2(
  settings: RequestSettings,
): Promise<RequestSettings> {
  const {
    requests_enabled,
    global_max_requests,
    global_window_days,
    global_auto_approval_enabled,
    force_dual_quality,
    watchlist_requests,
  } = settings;
  let etag = "";
  const body = await v2("PUT /api/v2/admin/request-settings", {
    headers: { "If-Match": requireETag(settings.etag) },
    body: {
      requests_enabled,
      global_max_requests,
      global_window_days,
      global_auto_approval_enabled,
      force_dual_quality,
      // Sent only when read: a server without the setting keeps its own.
      ...(watchlist_requests !== undefined && { watchlist_requests }),
    },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, updated_at: "", etag: requireETag(etag) };
}
/** How requests find their server: Standard, or Advanced with the rules. */
export type RequestRouting = Schemas["AdminRequestRouting"] & { etag: string };
export type RequestRoutingMode = RequestRouting["mode"];
export async function getAdminRequestRoutingV2(): Promise<RequestRouting> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-routing", {
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, etag: requireETag(etag) };
}
export async function putAdminRequestRoutingV2(
  mode: RequestRoutingMode,
  current: Pick<RequestRouting, "etag">,
): Promise<RequestRouting> {
  let etag = "";
  const body = await v2("PUT /api/v2/admin/request-routing", {
    headers: { "If-Match": requireETag(current.etag) },
    body: { mode },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, etag: requireETag(etag) };
}
export async function getAdminRequestUserLimitV2(userId: number): Promise<RequestUserLimit> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-users/{user_id}/limit", {
    path: { user_id: String(userId) },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, user_id: Number(body.user_id), etag: requireETag(etag) };
}
export async function putAdminRequestUserLimitV2(
  userId: number,
  limit: RequestUserLimit,
): Promise<RequestUserLimit> {
  let etag = "";
  const body = await v2("PUT /api/v2/admin/request-users/{user_id}/limit", {
    path: { user_id: String(userId) },
    headers: { "If-Match": requireETag(limit.etag) },
    body: {
      limit_mode: limit.limit_mode,
      approval_mode: limit.approval_mode,
      max_requests: limit.max_requests ?? null,
      window_days: limit.window_days ?? null,
    },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, user_id: Number(body.user_id), etag: requireETag(etag) };
}

/**
 * An access group's request approval and limit, with the validator of the
 * read it came from. A group with nothing saved reads as inherit at revision
 * zero, and its first save sends that tag.
 */
export type RequestGroupLimit = Omit<Schemas["AdminRequestGroupLimit"], "group_id"> & {
  group_id: number;
  etag: string;
};
export type RequestGroupLimitBody = Schemas["AdminRequestGroupLimitBody"];

/**
 * The validator names the profile that read it, so the limit is read and
 * saved under one captured authority.
 */
export async function getAdminRequestGroupLimitV2(
  groupId: number,
  profileContext?: ProfileRequestContextSnapshot,
): Promise<RequestGroupLimit> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-groups/{group_id}/limit", {
    path: { group_id: String(groupId) },
    profileContext,
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, group_id: Number(body.group_id), etag: requireETag(etag) };
}
export async function putAdminRequestGroupLimitV2(
  limit: Pick<RequestGroupLimit, "group_id" | "etag">,
  body: RequestGroupLimitBody,
  profileContext?: ProfileRequestContextSnapshot,
): Promise<RequestGroupLimit> {
  let etag = "";
  const saved = await v2("PUT /api/v2/admin/request-groups/{group_id}/limit", {
    path: { group_id: String(limit.group_id) },
    headers: { "If-Match": requireETag(limit.etag) },
    body,
    profileContext,
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...saved, group_id: Number(saved.group_id), etag: requireETag(etag) };
}
function integrationBody(
  integration: RequestIntegration,
): V2Body<"POST /api/v2/admin/request-integrations"> {
  return {
    name: integration.name,
    enabled: integration.enabled,
    base_url: integration.base_url,
    api_key_ref: integration.api_key_ref,
    capability_id: integration.capability_id ?? "",
    installation_id: String(integration.installation_id ?? ""),
    supported_media_types: integration.supported_media_types ?? [],
    plugin_config: integration.plugin_config ?? {},
  };
}
export async function getAdminRequestIntegrationV2(
  id: string,
  profileContext?: ProfileRequestContextSnapshot,
): Promise<RequestIntegration> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-integrations/{id}", {
    profileContext,
    path: { id },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return {
    ...body,
    installation_id: body.installation_id == null ? undefined : Number(body.installation_id),
    etag: requireETag(etag),
  };
}
export async function saveAdminRequestIntegrationV2(
  integration: RequestIntegration,
  create = false,
): Promise<RequestIntegration> {
  let etag = "";
  const onResponse = (r: Response) => {
    etag = r.headers.get("ETag") ?? "";
  };
  const body = create
    ? await v2("POST /api/v2/admin/request-integrations", {
        body: integrationBody(integration),
        onResponse,
      })
    : await v2("PUT /api/v2/admin/request-integrations/{id}", {
        path: { id: integration.id },
        headers: { "If-Match": requireETag(integration.etag) },
        body: integrationBody(integration),
        onResponse,
      });
  return {
    ...body,
    installation_id: body.installation_id == null ? undefined : Number(body.installation_id),
    etag: requireETag(etag),
  };
}
export function deleteAdminRequestIntegrationV2(
  integration: Pick<RequestIntegration, "id" | "etag">,
) {
  return v2("DELETE /api/v2/admin/request-integrations/{id}", {
    path: { id: integration.id },
    headers: { "If-Match": requireETag(integration.etag) },
  });
}
export async function listAdminRequestIntegrationsV2(): Promise<RequestIntegration[]> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const seen = new Set<string>();
  const ids: string[] = [];
  let cursor: string | undefined;
  do {
    if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
    const page = await v2("GET /api/v2/admin/request-integrations", {
      profileContext,
      query: { limit: 50, cursor },
    });
    ids.push(...page.items.map((i) => i.id));
    const next = page.page?.has_more ? page.page.next_cursor : undefined;
    if (!page.page?.has_more) break;
    if (!next || seen.has(next))
      throw new Error("Incomplete integration page. Reload to try again.");
    seen.add(next);
    cursor = next;
  } while (cursor);
  // Editor state must originate from a canonical per-row read with its validator.
  const rows = await Promise.all(
    [...new Set(ids)].map((id) => getAdminRequestIntegrationV2(id, profileContext)),
  );
  if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
  return rows;
}
/** A queue view: what an admin does next with the requests in it. */
export type AdminRequestQueueView = NonNullable<V2Query<"GET /api/v2/admin/requests">["view"]>;
export type AdminRequestCounts = Schemas["AdminRequestCounts"];
export type AdminRequestEvent = Schemas["AdminRequestEvent"];

export interface AdminRequestQueueFilter {
  view: AdminRequestQueueView;
  /** A title substring, or an exact TMDB ID. */
  q?: string;
  mediaType?: RequestMediaType;
  requestedByUserId?: number;
}

export interface AdminRequestQueuePage {
  items: MediaRequest[];
  /** Where the next page starts; absent on the last page. */
  nextCursor?: string;
}

/** One page of the admin queue, newest request first. */
export async function listAdminRequestQueuePageV2(
  filter: AdminRequestQueueFilter,
  options: { limit?: number; cursor?: string; signal?: AbortSignal } = {},
): Promise<AdminRequestQueuePage> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const q = filter.q?.trim();
  const page = await v2("GET /api/v2/admin/requests", {
    profileContext,
    signal: options.signal,
    query: {
      view: filter.view,
      q: q || undefined,
      media_type: filter.mediaType,
      requested_by_user_id:
        filter.requestedByUserId === undefined ? undefined : String(filter.requestedByUserId),
      limit: options.limit,
      cursor: options.cursor,
    },
  });
  if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
  if (page.page?.has_more && !page.page.next_cursor) {
    throw new Error("Incomplete request page. Reload to try again.");
  }
  return {
    items: page.items.map(mediaRequestFromV2),
    nextCursor: page.page?.has_more ? page.page.next_cursor : undefined,
  };
}
export function getAdminRequestCountsV2(): Promise<AdminRequestCounts> {
  return v2("GET /api/v2/admin/requests/counts");
}
/** A request's history, newest first. */
export function listAdminRequestEventsV2(id: string): Promise<AdminRequestEvent[]> {
  return v2("GET /api/v2/admin/requests/{id}/events", { path: { id } }).then(
    (result) => result.items,
  );
}
export function cancelAdminRequestV2(id: string, reason?: string) {
  return v2("POST /api/v2/admin/requests/{id}/cancel", { path: { id }, body: { reason } }).then(
    mediaRequestFromV2,
  );
}
export function approveAdminRequestV2(id: string) {
  return v2("POST /api/v2/admin/requests/{id}/approve", { path: { id }, body: {} }).then(
    mediaRequestFromV2,
  );
}
export function retryAdminRequestV2(id: string) {
  return v2("POST /api/v2/admin/requests/{id}/retry", { path: { id }, body: {} }).then(
    mediaRequestFromV2,
  );
}
export function declineAdminRequestV2(id: string, reason?: string) {
  return v2("POST /api/v2/admin/requests/{id}/decline", { path: { id }, body: { reason } }).then(
    mediaRequestFromV2,
  );
}
export async function getAdminRequestRouteV2(
  id: string,
  profileContext?: ProfileRequestContextSnapshot,
): Promise<RequestRoute> {
  let etag = "";
  const body = await v2("GET /api/v2/admin/request-routes/{id}", {
    profileContext,
    path: { id },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, etag: requireETag(etag) };
}
/**
 * Every route, in evaluation order per media type, each from its own read so
 * an editor always starts from a row and the validator that read returned.
 */
export async function listAdminRequestRoutesV2(): Promise<RequestRoute[]> {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const list = await v2("GET /api/v2/admin/request-routes", { profileContext });
  const rows = await Promise.all(
    list.items.map((route) => getAdminRequestRouteV2(route.id, profileContext)),
  );
  if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
  return rows;
}
export async function createAdminRequestRouteV2(body: RequestRouteBody): Promise<RequestRoute> {
  let etag = "";
  const saved = await v2("POST /api/v2/admin/request-routes", {
    body,
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...saved, etag: requireETag(etag) };
}
export async function updateAdminRequestRouteV2(
  route: Pick<RequestRoute, "id" | "etag">,
  body: RequestRouteBody,
): Promise<RequestRoute> {
  let etag = "";
  const saved = await v2("PUT /api/v2/admin/request-routes/{id}", {
    path: { id: route.id },
    headers: { "If-Match": requireETag(route.etag) },
    body,
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...saved, etag: requireETag(etag) };
}
export function deleteAdminRequestRouteV2(route: Pick<RequestRoute, "id" | "etag">) {
  return v2("DELETE /api/v2/admin/request-routes/{id}", {
    path: { id: route.id },
    headers: { "If-Match": requireETag(route.etag) },
  });
}
/** Sets the order of a media type's rules; `ids` lists every rule, fallback excluded. */
export function reorderAdminRequestRoutesV2(mediaType: RequestRouteMediaType, ids: string[]) {
  return v2("POST /api/v2/admin/request-routes/order", {
    body: { media_type: mediaType, ids },
  }).then((result) => result.items);
}
/**
 * Where each quality tier of a request for the title would go now. With a
 * requester, rules that match on the account apply as they would to that
 * account's request; without one they are skipped.
 */
export function previewAdminRequestRouteV2(
  mediaType: RequestRouteMediaType,
  tmdbId: number,
  requesterUserId?: number,
): Promise<RequestRoutePreview> {
  return v2("POST /api/v2/admin/request-routes/preview", {
    body: {
      media_type: mediaType,
      tmdb_id: tmdbId,
      requester_user_id: requesterUserId === undefined ? undefined : String(requesterUserId),
    },
  });
}
/**
 * Titles to try the routing rules on, from TMDB. Admin-only, and answers
 * whether or not requests are turned on.
 */
export function searchAdminRequestRouteTitlesV2(
  mediaType: RequestRouteMediaType,
  q: string,
  signal?: AbortSignal,
): Promise<RequestRouteTitle[]> {
  return v2("GET /api/v2/admin/request-routes/titles", {
    signal,
    query: { media_type: mediaType, q },
  }).then((result) => result.items);
}
export function loadAdminRequestIntegrationOptionsV2(
  id: string,
  body: LoadRequestIntegrationOptionsRequest,
) {
  return v2("POST /api/v2/admin/request-integrations/{id}/options", {
    path: { id },
    body: {
      base_url: body.base_url,
      api_key_ref: body.api_key_ref,
      capability_id: body.capability_id,
      installation_id: body.installation_id == null ? undefined : String(body.installation_id),
      plugin_config: body.plugin_config,
    },
  }).then((result) => result.options);
}
