import {
  captureProfileRequestContext,
  isProfileRequestContextCurrent,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type {
  LoadRequestIntegrationOptionsRequest,
  RequestIntegration,
  RequestListParams,
  RequestSettings,
  RequestUserLimit,
} from "@/api/types";
import { v2, type V2Body, type V2Result, V2ProblemError } from "./request";
import { mediaRequestFromV2 } from "./requests";

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
    },
    onResponse: (r) => {
      etag = r.headers.get("ETag") ?? "";
    },
  });
  return { ...body, updated_at: "", etag: requireETag(etag) };
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
export async function listAdminMediaRequestsV2(params: RequestListParams = {}) {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  const seen = new Set<string>();
  const wanted = Math.min(100, Math.max(1, params.limit ?? 50));
  const out = [];
  let cursor: string | undefined;
  while (out.length < wanted) {
    if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
    const page: V2Result<"GET /api/v2/admin/requests"> = await v2("GET /api/v2/admin/requests", {
      profileContext,
      query: {
        limit: Math.min(50, wanted - out.length),
        cursor,
        status: params.status && params.status !== "all" ? params.status : undefined,
        outcome: params.outcome && params.outcome !== "all" ? params.outcome : undefined,
      },
    });
    out.push(...page.items.map(mediaRequestFromV2));
    if (!page.page?.has_more) break;
    const next = page.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("Incomplete request page. Reload to try again.");
    seen.add(next);
    cursor = next;
  }
  if (!isProfileRequestContextCurrent(profileContext)) throw new StaleApiRequestContextError();
  return out;
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
