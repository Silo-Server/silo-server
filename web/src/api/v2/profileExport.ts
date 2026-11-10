import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { components } from "@/api/v2/schema";
import { v2 } from "./request";

export type ProfileExportCapability = components["schemas"]["ProfileExportCapability"];
export type ProfileImportResult = components["schemas"]["ProfileImportResult"];
export type ProfileImportData = ProfileImportResult["data"][number];

/** The file name the server gives an export. */
export const PROFILE_EXPORT_FILE_NAME = "silo-profile.json";

async function scoped<T>(context: ProfileRequestContextSnapshot, run: () => Promise<T>) {
  const result = await run();
  if (!isCapturedProfileAuthorityActive(context)) throw new StaleApiRequestContextError();
  return result;
}

export function getProfileExportCapability(context: ProfileRequestContextSnapshot) {
  return scoped(context, () =>
    v2("GET /api/v2/capabilities/profile-export", { profileContext: context }),
  );
}

/** The acting profile's `silo-profile-export` document. */
export function exportProfile(context: ProfileRequestContextSnapshot) {
  return scoped(context, () => v2("GET /api/v2/profile/export", { profileContext: context }));
}

/** Merges an export into the acting profile. The document never chooses the target. */
export function importProfile(
  context: ProfileRequestContextSnapshot,
  document: Record<string, unknown>,
) {
  return scoped(context, () =>
    v2("POST /api/v2/profile/import", {
      profileContext: context,
      body: document,
      retryAuthentication: false,
    }),
  );
}
