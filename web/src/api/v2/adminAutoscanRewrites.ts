import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AutoscanRewriteSuggestions } from "@/api/types";
import { v2 } from "./request";

export interface AutoscanRewriteIntent {
  sourceId: string;
  profileContext: ProfileRequestContextSnapshot;
}
export function captureAutoscanRewriteIntent(sourceId: string): AutoscanRewriteIntent {
  const profileContext = captureProfileRequestContext();
  if (!profileContext) throw new StaleApiRequestContextError();
  return { sourceId, profileContext };
}
export async function readAdminAutoscanRewrites(
  intent: AutoscanRewriteIntent,
): Promise<AutoscanRewriteSuggestions> {
  if (!isCapturedProfileAuthorityActive(intent.profileContext))
    throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/admin/autoscan/sources/{id}/rewrite-suggestions", {
    path: { id: intent.sourceId },
    profileContext: intent.profileContext,
    retryAuthentication: false,
  });
  if (!isCapturedProfileAuthorityActive(intent.profileContext))
    throw new StaleApiRequestContextError();
  return result;
}
