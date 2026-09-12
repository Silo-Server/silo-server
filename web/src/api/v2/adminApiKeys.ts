import { type ProfileRequestContextSnapshot } from "@/api/client";
import {
  captureAdminAuthority,
  adminAuthorityScope,
  requireAdminAuthority,
} from "./adminAuthority";
import type { components } from "./schema";
import { v2, type V2Body } from "./request";

export type AdminAPIKeyMetadata = components["schemas"]["AdminAPIKey"];
export type AdminAPIKeyListItem = components["schemas"]["AdminAPIKeyListItem"];
export type CreateBody = V2Body<"POST /api/v2/admin/api-keys">;
export type AdminAPIKeyEditor = {
  body: AdminAPIKeyMetadata;
  etag: string;
  profileContext: ProfileRequestContextSnapshot;
};
export type AdminAPIKeyPage = {
  items: AdminAPIKeyListItem[];
  page: { has_more: boolean; next_cursor?: string };
};
export const captureAdminApiKeyAuthority = captureAdminAuthority;
export const adminApiKeyScope = adminAuthorityScope;
const requireAuthority = requireAdminAuthority;
function strongETag(etag: string | null) {
  if (!etag || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(etag)) {
    throw new Error("Reload this API key before saving: a strong ETag is required.");
  }
  return etag;
}
export async function getAdminApiKeyCapabilities(profileContext = captureAdminApiKeyAuthority()) {
  requireAuthority(profileContext);
  const body = await v2("GET /api/v2/admin/api-keys/capabilities", { profileContext });
  requireAuthority(profileContext);
  return body;
}
export async function getAdminApiKey(
  id: string,
  profileContext = captureAdminApiKeyAuthority(),
): Promise<AdminAPIKeyEditor> {
  requireAuthority(profileContext);
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/api-keys/{id}", {
    path: { id },
    profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireAuthority(profileContext);
  return { body, etag: strongETag(etag), profileContext };
}
export async function listAdminApiKeysPage(
  cursor?: string,
  profileContext = captureAdminApiKeyAuthority(),
): Promise<AdminAPIKeyPage> {
  requireAuthority(profileContext);
  const body = await v2("GET /api/v2/admin/api-keys", {
    query: { limit: 50, cursor },
    profileContext,
  });
  requireAuthority(profileContext);
  const page = body.page;
  if (
    !Array.isArray(body.items) ||
    !page ||
    typeof page.has_more !== "boolean" ||
    (page.has_more &&
      (typeof page.next_cursor !== "string" || !page.next_cursor || page.next_cursor === cursor)) ||
    (!page.has_more && !!page.next_cursor)
  ) {
    throw new Error("Invalid API key pagination. Reload the list.");
  }
  return { items: body.items, page: { has_more: page.has_more, next_cursor: page.next_cursor } };
}
export async function createAdminApiKey(
  body: CreateBody,
  profileContext = captureAdminApiKeyAuthority(),
) {
  requireAuthority(profileContext);
  const created = await v2("POST /api/v2/admin/api-keys", {
    body: { label: body.label, user_id: body.user_id, scopes: body.scopes },
    profileContext,
    retryAuthentication: false,
  });
  requireAuthority(profileContext);
  return created;
}
export async function updateAdminApiKeyTier(
  editor: AdminAPIKeyEditor,
  tier: AdminAPIKeyMetadata["rate_tier"],
): Promise<AdminAPIKeyEditor> {
  const { profileContext } = editor;
  requireAuthority(profileContext);
  let etag: string | null = null;
  const body = await v2("PUT /api/v2/admin/api-keys/{id}/tier", {
    path: { id: editor.body.id },
    body: { rate_tier: tier },
    headers: { "If-Match": strongETag(editor.etag) },
    profileContext,
    retryAuthentication: false,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireAuthority(profileContext);
  return { body, etag: strongETag(etag), profileContext };
}
export async function deleteAdminApiKey(editor: AdminAPIKeyEditor) {
  requireAuthority(editor.profileContext);
  await v2("DELETE /api/v2/admin/api-keys/{id}", {
    path: { id: editor.body.id },
    headers: { "If-Match": strongETag(editor.etag) },
    profileContext: editor.profileContext,
    retryAuthentication: false,
  });
  requireAuthority(editor.profileContext);
}
