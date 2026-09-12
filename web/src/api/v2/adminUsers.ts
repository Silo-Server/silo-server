import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminUser, CreateUserRequest, UpdateUserRequest } from "@/api/types";
import { v2, type V2Body, type V2Result } from "./request";
import { sessionFromTokenPair } from "./account";
import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";
export type AdminUserEditor = {
  user: AdminUser;
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};
export const captureAdminUserAuthority = captureAdminAuthority;
export const adminUserScope = adminAuthorityScope;
export const requireAdminUserAuthority = requireAdminAuthority;
function numericID(value: string) {
  const id = Number(value);
  if (!Number.isSafeInteger(id) || id <= 0) throw new Error("Unsupported user ID.");
  return id;
}
function strongTag(value: string | null) {
  if (!value || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(value))
    throw new Error("Reload this user before saving: a strong ETag is required.");
  return value;
}
type AdminUserV2 = V2Result<"GET /api/v2/admin/users">["items"][number];

/** Projects canonical and list rows onto the numeric shape used by existing
 * admin pages and account pickers. IDs that cannot be represented exactly fail
 * before they can be used to address another account. */
export function adminUserFromV2(user: AdminUserV2): AdminUser {
  return {
    id: numericID(user.id),
    username: user.username,
    email: user.email,
    role: user.role,
    permissions: user.permissions,
    enabled: user.enabled,
    library_ids: user.library_ids === null ? null : user.library_ids.map(numericID),
    access_group_id: user.access_group_id === null ? null : numericID(user.access_group_id),
    max_playback_quality: user.max_playback_quality,
    max_streams: user.max_streams,
    max_transcodes: user.max_transcodes,
    transcode_allowed: user.transcode_allowed,
    audio_transcode_allowed: user.audio_transcode_allowed,
    max_profiles: user.max_profiles,
    download_allowed: user.download_allowed,
    download_transcode_allowed: user.download_transcode_allowed,
    requests_allowed: user.requests_allowed,
    effective_policy: {
      library_ids:
        user.effective_policy.library_ids === null
          ? null
          : user.effective_policy.library_ids.map(numericID),
      max_playback_quality: user.effective_policy.max_playback_quality,
      max_streams: user.effective_policy.max_streams,
      max_transcodes: user.effective_policy.max_transcodes,
      transcode_allowed: user.effective_policy.transcode_allowed,
      audio_transcode_allowed: user.effective_policy.audio_transcode_allowed,
      download_allowed: user.effective_policy.download_allowed,
      download_transcode_allowed: user.effective_policy.download_transcode_allowed,
      requests_allowed: user.effective_policy.requests_allowed,
      permissions: user.effective_policy.permissions,
    },
    created_at: user.created_at,
    updated_at: user.updated_at,
    ...(user.last_active_at === null ? {} : { last_active_at: user.last_active_at }),
  };
}

// Existing admin account pickers require a complete array. Keep one authority
// for the bounded page walk and reject incomplete traversals instead of caching a subset.
export async function listAdminUsers(
  profileContext = captureAdminUserAuthority(),
  signal?: AbortSignal,
): Promise<AdminUser[]> {
  const users: AdminUser[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  do {
    requireAdminUserAuthority(profileContext);
    const body = await v2("GET /api/v2/admin/users", {
      query: { limit: 200, cursor },
      profileContext,
      signal,
    });
    requireAdminUserAuthority(profileContext);
    if (!Array.isArray(body.items) || !body.page || typeof body.page.has_more !== "boolean")
      throw new Error("Invalid user pagination. Reload the list.");
    users.push(...body.items.map(adminUserFromV2));
    if (!body.page.has_more) {
      if (body.page.next_cursor) throw new Error("Invalid user pagination. Reload the list.");
      return users;
    }
    const next = body.page.next_cursor;
    if (typeof next !== "string" || !next || seen.has(next))
      throw new Error("Invalid user pagination. Reload the list.");
    seen.add(next);
    cursor = next;
  } while (cursor !== undefined);
  return users;
}
export async function getAdminUser(
  id: number,
  profileContext = captureAdminUserAuthority(),
): Promise<AdminUserEditor> {
  requireAdminUserAuthority(profileContext);
  let etag: string | null = null;
  const user = await v2("GET /api/v2/admin/users/{id}", {
    path: { id: String(id) },
    profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireAdminUserAuthority(profileContext);
  return { user: adminUserFromV2(user), etag: strongTag(etag), profileContext };
}
function policyWire(body: UpdateUserRequest) {
  return {
    ...body,
    library_ids: body.library_ids == null ? body.library_ids : body.library_ids.map(String),
    access_group_id:
      body.access_group_id == null ? body.access_group_id : String(body.access_group_id),
  };
}
export async function createAdminUser(
  input: CreateUserRequest,
  profileContext = captureAdminUserAuthority(),
) {
  requireAdminUserAuthority(profileContext);
  const body = {
    ...policyWire(input),
    username: input.username,
    email: input.email,
    password: input.password,
    role: input.role as "admin" | "user",
    create_default_profile: input.create_default_profile ?? true,
  } satisfies V2Body<"POST /api/v2/admin/users">;
  const created = await v2("POST /api/v2/admin/users", {
    body,
    profileContext,
    retryAuthentication: false,
  });
  requireAdminUserAuthority(profileContext);
  return { id: numericID(created.id) };
}
export async function updateAdminUser(editor: AdminUserEditor, input: UpdateUserRequest) {
  requireAdminUserAuthority(editor.profileContext);
  const body = {
    ...policyWire(input),
    role: input.role as "admin" | "user" | undefined,
  } satisfies V2Body<"PUT /api/v2/admin/users/{id}">;
  await v2("PUT /api/v2/admin/users/{id}", {
    path: { id: String(editor.user.id) },
    body,
    headers: { "If-Match": strongTag(editor.etag) },
    profileContext: editor.profileContext,
    retryAuthentication: false,
  });
  requireAdminUserAuthority(editor.profileContext);
}
export async function deleteAdminUser(editor: AdminUserEditor) {
  requireAdminUserAuthority(editor.profileContext);
  await v2("DELETE /api/v2/admin/users/{id}", {
    path: { id: String(editor.user.id) },
    headers: { "If-Match": strongTag(editor.etag) },
    profileContext: editor.profileContext,
    retryAuthentication: false,
  });
  requireAdminUserAuthority(editor.profileContext);
}
export async function impersonateAdminUser(
  id: number,
  profileContext = captureAdminUserAuthority(),
) {
  requireAdminUserAuthority(profileContext);
  const pair = await v2("POST /api/v2/admin/users/{id}/impersonate", {
    path: { id: String(id) },
    profileContext,
    retryAuthentication: false,
  });
  requireAdminUserAuthority(profileContext);
  return { session: sessionFromTokenPair(pair), profileContext };
}
export async function getAdminUserCapabilities(profileContext = captureAdminUserAuthority()) {
  requireAdminUserAuthority(profileContext);
  const result = await v2("GET /api/v2/admin/users/capabilities", { profileContext });
  requireAdminUserAuthority(profileContext);
  return result;
}
