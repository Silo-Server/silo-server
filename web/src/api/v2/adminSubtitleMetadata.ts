import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";
import { v2, type V2Body, type V2Result } from "./request";

export type AdminSubtitleEditIntent = {
  subtitle: AdminStoredSubtitle;
  profileContext: ProfileRequestContextSnapshot;
  scope: string;
};
export type AdminSubtitleEditor = {
  intent: AdminSubtitleEditIntent;
  body: V2Result<"GET /api/v2/admin/subtitles/{id}">;
  etag: string;
};
export type AdminSubtitlePatch = V2Body<"PATCH /api/v2/admin/subtitles/{id}">;
export function captureAdminSubtitleEditIntent(
  subtitle: AdminStoredSubtitle,
  scope: string,
): AdminSubtitleEditIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext || adminSubtitleListScope() !== scope)
    throw new StaleApiRequestContextError();
  return { subtitle, profileContext, scope };
}
function requireIntent(intent: AdminSubtitleEditIntent) {
  if (
    !isCapturedProfileAuthorityActive(intent.profileContext) ||
    adminSubtitleListScope() !== intent.scope
  )
    throw new StaleApiRequestContextError();
}
function requireTag(value: string | null): string {
  if (!value || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(value))
    throw new Error("A strong subtitle edit validator is required. Reopen the editor.");
  return value;
}
function requireIdentity(body: AdminSubtitleEditor["body"], intent: AdminSubtitleEditIntent) {
  if (body.id !== intent.subtitle.id || body.media_file_id !== intent.subtitle.media_file_id)
    throw new Error("Subtitle identity changed. Reload the list.");
}
export async function getAdminSubtitleEditor(
  intent: AdminSubtitleEditIntent,
  signal?: AbortSignal,
): Promise<AdminSubtitleEditor> {
  requireIntent(intent);
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/subtitles/{id}", {
    path: { id: intent.subtitle.id },
    profileContext: intent.profileContext,
    signal,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireIntent(intent);
  requireIdentity(body, intent);
  return { intent, body, etag: requireTag(etag) };
}
export async function updateAdminSubtitleMetadata(
  editor: AdminSubtitleEditor,
  patch: AdminSubtitlePatch,
): Promise<AdminSubtitleEditor> {
  requireIntent(editor.intent);
  let etag: string | null = null;
  const body = await v2("PATCH /api/v2/admin/subtitles/{id}", {
    path: { id: editor.intent.subtitle.id },
    body: patch,
    headers: { "If-Match": requireTag(editor.etag) },
    profileContext: editor.intent.profileContext,
    retryAuthentication: false,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireIntent(editor.intent);
  requireIdentity(body, editor.intent);
  return { intent: editor.intent, body, etag: requireTag(etag) };
}
