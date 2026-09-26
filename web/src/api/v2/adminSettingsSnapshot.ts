import {
  captureProfileRequestContext,
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import { adminKeys } from "@/hooks/queries/keys";
import { v2 } from "./request";

export type SettingsValues = Record<string, string>;
export type SettingsBaseline = { etag: string; profileContext: ProfileRequestContextSnapshot };
const baselines = new WeakMap<SettingsValues, SettingsBaseline>();
export function adminSettingsKey(context: ProfileRequestContextSnapshot) {
  return [
    ...adminKeys.serverSettings(),
    context.serverOrigin,
    context.authContextVersion,
    context.profileId,
  ] as const;
}
export async function readAdminSettings(
  profileContext: ProfileRequestContextSnapshot,
): Promise<SettingsValues> {
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  let etag = "";
  const values = await v2("GET /api/v2/admin/settings/effective", {
    profileContext,
    onResponse: (response) => {
      etag = response.headers.get("ETag") ?? "";
    },
  });
  if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
  if (!etag || etag === "*" || etag.startsWith("W/"))
    throw new Error("Settings response is missing its version.");
  baselines.set(values, { etag, profileContext });
  return values;
}
export function captureSettingsBaseline(values: SettingsValues | undefined): SettingsBaseline {
  const baseline = values && baselines.get(values);
  if (!baseline) throw new Error("Reload settings before saving.");
  if (!isCapturedProfileAuthorityActive(baseline.profileContext) || !captureProfileRequestContext())
    throw new StaleApiRequestContextError();
  return baseline;
}
