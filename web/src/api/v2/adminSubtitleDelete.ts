import { isCapturedProfileAuthorityActive, StaleApiRequestContextError } from "@/api/client";
import { adminSubtitleListScope, type AdminStoredSubtitle } from "./adminSubtitles";
import { captureAdminSubtitleEditIntent, getAdminSubtitleEditor } from "./adminSubtitleMetadata";
import { v2 } from "./request";

// Each explicit confirmation owns one original validator and at most one DELETE.
export async function prepareAdminSubtitleDeletion(
  subtitle: AdminStoredSubtitle,
  scope: string,
  signal?: AbortSignal,
) {
  const editor = await getAdminSubtitleEditor(
    captureAdminSubtitleEditIntent(subtitle, scope),
    signal,
  );
  let consumed = false;
  return {
    subtitle: editor.body,
    async confirm() {
      if (consumed)
        throw new Error(
          "This deletion was already attempted. Close and review the subtitle again.",
        );
      if (
        !isCapturedProfileAuthorityActive(editor.intent.profileContext) ||
        adminSubtitleListScope() !== scope
      )
        throw new StaleApiRequestContextError();
      consumed = true;
      await v2("DELETE /api/v2/admin/subtitles/{id}", {
        path: { id: editor.body.id },
        headers: { "If-Match": editor.etag },
        profileContext: editor.intent.profileContext,
        retryAuthentication: false,
      });
      if (
        !isCapturedProfileAuthorityActive(editor.intent.profileContext) ||
        adminSubtitleListScope() !== scope
      )
        throw new StaleApiRequestContextError();
    },
  };
}
export type AdminSubtitleDeletion = Awaited<ReturnType<typeof prepareAdminSubtitleDeletion>>;
