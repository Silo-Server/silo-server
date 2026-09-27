import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AutoscanSettings, AutoscanStatus } from "@/api/types";
import { v2 } from "./request";

export async function readAdminAutoscanSettings(
  profileContext: ProfileRequestContextSnapshot,
): Promise<AutoscanSettings> {
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/admin/autoscan/settings", { profileContext });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return result;
}
export async function readAdminAutoscanStatus(
  profileContext: ProfileRequestContextSnapshot,
): Promise<AutoscanStatus> {
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  const result = await v2("GET /api/v2/admin/autoscan/status", { profileContext });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  return {
    ...result,
    sources: result.sources.map((row) => ({
      ...row,
      last_run_at: row.last_run_at ?? null,
      last_error: row.last_error ?? null,
    })),
  };
}
