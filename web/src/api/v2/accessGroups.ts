import { type ProfileRequestContextSnapshot } from "@/api/client";
import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";
import type { AccessGroup, AccessGroupInput } from "@/api/types";
import type { components } from "./schema";
import { v2, type V2Body } from "./request";

type Group = components["schemas"]["AdminAccessGroup"];
export type AccessGroupEditor = {
  group: AccessGroup;
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};
export const captureAccessGroupAuthority = captureAdminAuthority;
export const accessGroupScope = adminAuthorityScope;
const requireAuthority = requireAdminAuthority;
function numericID(value: string) {
  const id = Number(value);
  if (!Number.isSafeInteger(id) || id <= 0)
    throw new Error("This group contains an unsupported ID.");
  return id;
}
function legacy(group: Group, memberCount = 0): AccessGroup {
  return {
    ...group,
    id: numericID(group.id),
    library_ids: group.library_ids?.map(numericID) ?? null,
    allowed_permissions: group.allowed_permissions ?? null,
    member_count: memberCount,
  };
}
function strongTag(value: string | null) {
  if (!value || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(value))
    throw new Error("Reload this group before saving: a strong ETag is required.");
  return value;
}
function wire(body: AccessGroupInput): V2Body<"POST /api/v2/admin/access-groups"> {
  return {
    ...body,
    library_ids: body.library_ids == null ? body.library_ids : body.library_ids.map(String),
  };
}
// AdminUsers, AdminUserDetail, and InvitationsTab selectors require the complete
// small configuration set. Each bounded
// page stays under one authority, and a failed traversal never returns a subset.
export async function listAccessGroups(context = captureAccessGroupAuthority()) {
  const result: AccessGroup[] = [];
  const seen = new Set<string>();
  let cursor: string | undefined;
  do {
    requireAuthority(context);
    const body = await v2("GET /api/v2/admin/access-groups", {
      query: { limit: 200, cursor },
      profileContext: context,
    });
    requireAuthority(context);
    if (!Array.isArray(body.items) || !body.page || typeof body.page.has_more !== "boolean")
      throw new Error("Invalid group pagination. Reload the list.");
    result.push(...body.items.map((item) => legacy(item, item.member_count)));
    if (!body.page.has_more) {
      if (body.page.next_cursor) throw new Error("Invalid group pagination. Reload the list.");
      break;
    }
    const next = body.page.next_cursor;
    if (typeof next !== "string" || !next || seen.has(next))
      throw new Error("Invalid group pagination. Reload the list.");
    seen.add(next);
    cursor = next;
  } while (cursor !== undefined);
  return result.sort(
    (a, b) => a.name.toLowerCase().localeCompare(b.name.toLowerCase()) || a.id - b.id,
  );
}
export async function getAccessGroup(
  id: number,
  profileContext = captureAccessGroupAuthority(),
): Promise<AccessGroupEditor> {
  requireAuthority(profileContext);
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/access-groups/{id}", {
    path: { id: String(id) },
    profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireAuthority(profileContext);
  return { group: legacy(body), etag: strongTag(etag), profileContext };
}
export async function createAccessGroup(
  body: AccessGroupInput,
  profileContext = captureAccessGroupAuthority(),
) {
  requireAuthority(profileContext);
  const group = await v2("POST /api/v2/admin/access-groups", {
    body: wire(body),
    profileContext,
    retryAuthentication: false,
  });
  requireAuthority(profileContext);
  return legacy(group);
}
export async function updateAccessGroup(
  editor: AccessGroupEditor,
  input: AccessGroupInput,
): Promise<AccessGroupEditor> {
  const { profileContext } = editor;
  requireAuthority(profileContext);
  let etag: string | null = null;
  const body = await v2("PUT /api/v2/admin/access-groups/{id}", {
    path: { id: String(editor.group.id) },
    body: wire(input),
    headers: { "If-Match": strongTag(editor.etag) },
    profileContext,
    retryAuthentication: false,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireAuthority(profileContext);
  return { group: legacy(body), etag: strongTag(etag), profileContext };
}
export async function deleteAccessGroup(editor: AccessGroupEditor) {
  requireAuthority(editor.profileContext);
  await v2("DELETE /api/v2/admin/access-groups/{id}", {
    path: { id: String(editor.group.id) },
    headers: { "If-Match": strongTag(editor.etag) },
    profileContext: editor.profileContext,
    retryAuthentication: false,
  });
  requireAuthority(editor.profileContext);
}

export async function getAccessGroupCapabilities(profileContext = captureAccessGroupAuthority()) {
  requireAuthority(profileContext);
  const body = await v2("GET /api/v2/admin/users/capabilities", { profileContext });
  requireAuthority(profileContext);
  return body;
}
