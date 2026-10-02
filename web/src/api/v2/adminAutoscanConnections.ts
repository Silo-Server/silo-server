import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AutoscanConnection } from "@/api/types";
import { v2 } from "./request";
import { readAutoscanPages } from "./adminAutoscanPagination";

export async function readAdminAutoscanConnections(
  profileContext: ProfileRequestContextSnapshot,
): Promise<AutoscanConnection[]> {
  return readAutoscanPages(
    profileContext,
    (cursor) =>
      v2("GET /api/v2/admin/autoscan/connections", {
        profileContext,
        query: { limit: 100, cursor },
      }),
    (items) => items,
    "Invalid autoscan connection page.",
    "Invalid autoscan connection continuation.",
    "Too many autoscan connections to display.",
  );
}
