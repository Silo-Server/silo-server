import type { components } from "./schema";
import { v2 } from "./request";

export type PersonalAPIKey = components["schemas"]["PersonalAPIKeyListItem"];
export type PersonalAPIKeyScopes = components["schemas"]["PersonalAPIKeyScopesOutputBody"];
export type PersonalAPIKeyPage = { items: PersonalAPIKey[]; nextCursor?: string };

/** Which scopes exist and whether this server lets users create keys. */
export function getPersonalApiKeyScopes() {
  return v2("GET /api/v2/api-keys/scopes");
}

/** One page of the keys the acting profile may see. */
export async function listPersonalApiKeysPage(cursor?: string): Promise<PersonalAPIKeyPage> {
  const body = await v2("GET /api/v2/api-keys", { query: { limit: 50, cursor } });
  const next = body.page?.has_more ? body.page.next_cursor : undefined;
  // A cursor that repeats would page forever.
  return { items: body.items, nextCursor: next && next !== cursor ? next : undefined };
}

/** Creates a key; `profileId` binds it to that profile. The key is in the result once. */
export function createPersonalApiKey(input: {
  label: string;
  scopes: string[];
  profileId?: string;
}) {
  return v2("POST /api/v2/api-keys", {
    body: {
      label: input.label,
      scopes: input.scopes,
      ...(input.profileId ? { profile_id: input.profileId } : {}),
    },
    // Creation is not retryable: a replay could mint a second key.
    retryAuthentication: false,
  });
}

export function revokePersonalApiKey(id: string) {
  return v2("DELETE /api/v2/api-keys/{id}", { path: { id } });
}
