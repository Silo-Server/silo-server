import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { v2, type V2Query } from "./request";
import type { components } from "./schema";

export type AdminStoredSubtitle = components["schemas"]["AdminStoredSubtitle"];
export type AdminSubtitleListQuery = V2Query<"GET /api/v2/admin/subtitles">;

// Cache keys contain an opaque generation, never a bearer or PIN token.
let lastAuthority: ProfileRequestContextSnapshot | null = null;
let generation = 0;
export function adminSubtitleListScope() {
  const current = captureProfileRequestContext();
  if (
    current?.authContextVersion !== lastAuthority?.authContextVersion ||
    current?.serverOrigin !== lastAuthority?.serverOrigin ||
    current?.profileId !== lastAuthority?.profileId ||
    current?.profileToken !== lastAuthority?.profileToken
  ) {
    generation++;
    lastAuthority = current;
  }
  return `subtitle-list:${generation}`;
}

export async function listAdminSubtitles(
  query: AdminSubtitleListQuery,
  scope: string,
  signal?: AbortSignal,
) {
  const context = captureProfileRequestContext();
  if (!context || adminSubtitleListScope() !== scope) throw new StaleApiRequestContextError();
  const body = await v2("GET /api/v2/admin/subtitles", { query, profileContext: context, signal });
  if (!isCapturedProfileAuthorityActive(context) || adminSubtitleListScope() !== scope)
    throw new StaleApiRequestContextError();
  if (
    !Array.isArray(body.items) ||
    !body.page ||
    typeof body.page.has_more !== "boolean" ||
    (body.page.has_more &&
      (!body.items.length || !body.page.next_cursor || body.page.next_cursor === query.cursor)) ||
    (!body.page.has_more && !!body.page.next_cursor) ||
    body.items.some(
      (item) =>
        typeof item.id !== "string" ||
        !/^[1-9]\d*$/.test(item.id) ||
        typeof item.media_file_id !== "string" ||
        !/^[1-9]\d*$/.test(item.media_file_id),
    )
  )
    throw new Error("Invalid subtitle list. Reload the page.");
  return body;
}
