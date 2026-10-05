import { v2 } from "@/api/v2/request";

/** Whether profiles may add rule rows, and the version a change must be checked against. */
export interface AdminSectionSettingsSnapshot {
  allowProfileRuleRows: boolean;
  etag: string;
}

function requiredETag(etag: string | null | undefined): string {
  if (!etag || etag === "*")
    throw new Error("Reload this setting before changing it; its version is unavailable.");
  return etag;
}

export async function fetchAdminSectionSettings(
  signal?: AbortSignal,
): Promise<AdminSectionSettingsSnapshot> {
  let etag: string | null = null;
  const body = await v2("GET /api/v2/admin/settings/sections", {
    signal,
    onResponse: (response) => {
      etag = response.headers.get("ETag");
    },
  });
  return { allowProfileRuleRows: body.allow_profile_custom_sections, etag: requiredETag(etag) };
}

/**
 * Writes over the version that was read; a stale one fails with 412 instead of
 * overwriting. The new version is null when the response carried none, so the
 * caller reads the setting again rather than calling a saved change a failure.
 */
export async function updateAdminSectionSettings(
  allowProfileRuleRows: boolean,
  etag: string,
): Promise<{ allowProfileRuleRows: boolean; etag: string | null }> {
  const ifMatch = requiredETag(etag);
  let next = null as string | null;
  const body = await v2("PUT /api/v2/admin/settings/sections", {
    headers: { "If-Match": ifMatch },
    body: { allow_profile_custom_sections: allowProfileRuleRows },
    onResponse: (response) => {
      next = response.headers.get("ETag");
    },
  });
  return {
    allowProfileRuleRows: body.allow_profile_custom_sections,
    etag: next && next !== "*" ? next : null,
  };
}
