import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { adminSubtitleListScope } from "./adminSubtitles";
import { v2, type V2Body, type V2Result } from "./request";

export type ProviderEditIntent = {
  provider: string;
  scope: string;
  profileContext: ProfileRequestContextSnapshot;
};
export type ProviderEditor = {
  intent: ProviderEditIntent;
  body: V2Result<"GET /api/v2/admin/subtitle-providers/{provider}">;
  etag: string;
};
export type ProviderChange = V2Body<"PUT /api/v2/admin/subtitle-providers/{provider}">;
export function captureProviderEditIntent(provider: string, scope: string): ProviderEditIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext || adminSubtitleListScope() !== scope)
    throw new StaleApiRequestContextError();
  return { provider, scope, profileContext };
}
export function providerIntentActive(intent: ProviderEditIntent) {
  return (
    isCapturedProfileAuthorityActive(intent.profileContext) &&
    adminSubtitleListScope() === intent.scope
  );
}
function requireIntent(intent: ProviderEditIntent) {
  if (!providerIntentActive(intent)) throw new StaleApiRequestContextError();
}
function requireTag(tag: string | null): string {
  if (!tag || !/^"[\x21\x23-\x7e\x80-\xff]*"$/.test(tag))
    throw new Error("A strong edit validator is required. Reload saved configuration.");
  return tag;
}
export async function listSubtitleProviders(scope: string, signal?: AbortSignal) {
  const intent = captureProviderEditIntent("", scope);
  const body = await v2("GET /api/v2/admin/subtitle-providers", {
    profileContext: intent.profileContext,
    signal,
  });
  requireIntent(intent);
  return body;
}
export async function getProviderEditor(intent: ProviderEditIntent): Promise<ProviderEditor> {
  requireIntent(intent);
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/subtitle-providers/{provider}", {
    path: { provider: intent.provider },
    profileContext: intent.profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  requireIntent(intent);
  if (
    body.provider_name !== intent.provider ||
    typeof body.enabled !== "boolean" ||
    typeof body.has_api_key !== "boolean" ||
    typeof body.has_credentials !== "boolean"
  )
    throw new Error("Invalid provider configuration. Reload saved configuration.");
  return { intent, body, etag: requireTag(etag) };
}
export async function saveProviderConfiguration(editor: ProviderEditor, body: ProviderChange) {
  requireIntent(editor.intent);
  const saved = await v2("PUT /api/v2/admin/subtitle-providers/{provider}", {
    path: { provider: editor.intent.provider },
    body,
    headers: { "If-Match": requireTag(editor.etag) },
    profileContext: editor.intent.profileContext,
    retryAuthentication: false,
  });
  requireIntent(editor.intent);
  if (
    typeof saved.saved_revision !== "string" ||
    !/^[1-9]\d*$/.test(saved.saved_revision) ||
    !["applied", "not_configured", "unsupported", "failed"].includes(saved.local_apply) ||
    (saved.local_applied_revision !== undefined &&
      (typeof saved.local_applied_revision !== "string" ||
        !/^(0|[1-9]\d*)$/.test(saved.local_applied_revision))) ||
    (saved.local_apply === "applied" && saved.local_applied_revision === undefined)
  )
    throw new Error(
      "Unable to confirm the save result. Reload saved configuration before retrying.",
    );
  return saved;
}
export function providerSaveMessage(
  saved: Pick<
    Awaited<ReturnType<typeof saveProviderConfiguration>>,
    "saved_revision" | "local_apply" | "local_applied_revision"
  >,
) {
  if (saved.local_apply === "applied") {
    return saved.local_applied_revision === saved.saved_revision
      ? "Settings saved and applied on this server. Other servers may still use older settings."
      : "Settings saved. This server applied a different saved revision; reload to review the current configuration.";
  }
  return "Settings saved, but not applied on this server. Other servers may still use older settings.";
}
