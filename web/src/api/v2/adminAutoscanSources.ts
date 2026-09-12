import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AutoscanSource } from "@/api/types";
import { v2 } from "./request";
import { readAutoscanPages } from "./adminAutoscanPagination";

export async function readAdminAutoscanSources(
  profileContext: ProfileRequestContextSnapshot,
): Promise<AutoscanSource[]> {
  return readAutoscanPages(
    profileContext,
    (cursor) =>
      v2("GET /api/v2/admin/autoscan/sources", { profileContext, query: { limit: 100, cursor } }),
    (items) =>
      items.map((row) => ({
        ...row,
        poll_interval_seconds: row.poll_interval_seconds ?? null,
        last_run_at: row.last_run_at ?? null,
        last_error: row.last_error ?? null,
      })),
    "Invalid autoscan source page.",
    "Invalid autoscan source continuation.",
    "Too many autoscan sources to display.",
  );
}
