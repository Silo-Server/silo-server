import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2 } from "./request";
export interface AdminSettingIdentity {
  scope:
    | "account"
    | "profile"
    | "profile_client"
    | "profile_device"
    | "profile_library"
    | "profile_series";
  profileId?: string;
  clientFamily?: "tv" | "mobile" | "tablet" | "desktop" | "web";
  deviceId?: string;
  libraryId?: number;
  seriesId?: string;
}
export function captureAdminSettingAuthority() {
  const authority = captureProfileRequestContext();
  if (!authority) throw new StaleApiRequestContextError();
  return authority;
}
function current(authority: ProfileRequestContextSnapshot) {
  if (!isCapturedProfileAuthorityActive(authority)) throw new StaleApiRequestContextError();
}
function query(identity: AdminSettingIdentity) {
  return {
    scope: identity.scope,
    profile_id: identity.profileId,
    client_family: identity.clientFamily,
    device_id: identity.deviceId,
    library_id: identity.libraryId === undefined ? undefined : String(identity.libraryId),
    series_id: identity.seriesId,
  };
}
function numericLibraryID(value: string) {
  const id = Number(value);
  if (!Number.isSafeInteger(id) || id <= 0)
    throw new Error("Unsupported library ID in user settings.");
  return id;
}
export async function listAdminSettingValues(
  userId: number,
  authority = captureAdminSettingAuthority(),
) {
  const values = [];
  const visited = new Set<string>();
  let cursor: string | undefined;
  let revision = 0;
  do {
    current(authority);
    const page = await v2("GET /api/v2/admin/users/{id}/settings/values", {
      path: { id: String(userId) },
      query: { limit: 200, cursor },
      profileContext: authority,
    });
    current(authority);
    if (
      !Array.isArray(page.items) ||
      !page.page ||
      typeof page.page.has_more !== "boolean" ||
      (page.page.has_more &&
        (typeof page.page.next_cursor !== "string" ||
          !page.page.next_cursor ||
          visited.has(page.page.next_cursor))) ||
      (!page.page.has_more && page.page.next_cursor)
    )
      throw new Error("Invalid settings page. Reload the settings.");
    revision = page.revision;
    for (const row of page.items)
      values.push({
        ...row,
        scope: row.scope as AdminSettingIdentity["scope"],
        client_family: row.client_family as AdminSettingIdentity["clientFamily"],
        library_id: row.library_id === undefined ? undefined : numericLibraryID(row.library_id),
      });
    cursor = page.page.has_more ? page.page.next_cursor : undefined;
    if (cursor) visited.add(cursor);
  } while (cursor);
  return { values, revision };
}
export async function setAdminSettingValue(
  userId: number,
  key: string,
  identity: AdminSettingIdentity,
  value: unknown,
  authority = captureAdminSettingAuthority(),
) {
  current(authority);
  const result = await v2("PUT /api/v2/admin/users/{id}/settings/values/{key}", {
    path: { id: String(userId), key },
    query: query(identity),
    body: { value },
    profileContext: authority,
    retryAuthentication: false,
  });
  current(authority);
  return result;
}
export async function deleteAdminSettingValue(
  userId: number,
  key: string,
  identity: AdminSettingIdentity,
  authority = captureAdminSettingAuthority(),
) {
  current(authority);
  await v2("DELETE /api/v2/admin/users/{id}/settings/values/{key}", {
    path: { id: String(userId), key },
    query: query(identity),
    profileContext: authority,
    retryAuthentication: false,
  });
  current(authority);
}
