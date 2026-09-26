import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AutoscanScan, AutoscanScanStatus, AutoscanEventStatus } from "@/api/types";
import { v2 } from "./request";
export type AutoscanScanQuery = {
  status?: AutoscanScanStatus;
  query?: string;
  limit?: number;
  offset?: number;
  enabled?: boolean;
};
function safeID(value: string): number {
  const n = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(n))
    throw new Error("Invalid scan history identity.");
  return n;
}
export async function readAdminAutoscanScans(
  profileContext: ProfileRequestContextSnapshot,
  params: AutoscanScanQuery,
): Promise<{ rows: AutoscanScan[]; total: number }> {
  const limit = params.limit ?? 50;
  const offset = params.offset ?? 0;
  if (
    !Number.isInteger(limit) ||
    limit < 1 ||
    limit > 200 ||
    !Number.isInteger(offset) ||
    offset < 0 ||
    offset % limit !== 0 ||
    offset / limit >= 100
  )
    throw new Error("Scan history page is outside the supported range.");
  let cursor: string | undefined;
  const seen = new Set<string>();
  for (let page = 0; page <= offset / limit; page++) {
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    const result = await v2("GET /api/v2/admin/autoscan/scans", {
      profileContext,
      query: { limit, cursor, status: params.status, q: params.query },
    });
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    if (
      !result.page ||
      typeof result.page.has_more !== "boolean" ||
      !Number.isSafeInteger(result.total) ||
      result.total < 0 ||
      result.items.length > limit
    )
      throw new Error("Invalid scan history page.");
    const next = result.page.next_cursor;
    if (result.page.has_more && (!next || seen.has(next)))
      throw new Error("Invalid scan history continuation.");
    if (page === offset / limit) {
      const ids = new Set<string>();
      const rows: AutoscanScan[] = result.items.map((row) => {
        if (!row.id || ids.has(row.id)) throw new Error("Invalid scan history identity.");
        ids.add(row.id);
        if (
          !["library", "subtree", "file"].includes(row.mode) ||
          !["accepted", "running", "completed", "failed", "cancelled"].includes(row.status)
        )
          throw new Error("Unsupported scan history state.");
        if (
          row.event_status &&
          !["running", "success", "error", "unresolved"].includes(row.event_status)
        )
          throw new Error("Unsupported scan event state.");
        return {
          ...row,
          library_id: safeID(row.library_id),
          autoscan_event_id:
            row.autoscan_event_id == null ? undefined : safeID(row.autoscan_event_id),
          mode: row.mode as AutoscanScan["mode"],
          status: row.status as AutoscanScanStatus,
          event_status: row.event_status as AutoscanEventStatus | undefined,
          source_id: row.source_id ?? undefined,
        };
      });
      return { rows, total: result.total };
    }
    if (!result.page.has_more) return { rows: [], total: result.total };
    seen.add(next!);
    cursor = next;
  }
  throw new Error("Scan history page unavailable.");
}
