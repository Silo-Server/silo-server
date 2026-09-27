import {
  isCapturedProfileAuthorityActive,
  StaleApiRequestContextError,
  type ProfileRequestContextSnapshot,
} from "@/api/client";
import type { AutoscanEvent, AutoscanEventStatus, AutoscanEventScanRun } from "@/api/types";
import { v2 } from "./request";
export type AutoscanEventQuery = {
  sourceId?: string;
  status?: AutoscanEventStatus;
  query?: string;
  limit?: number;
  offset?: number;
  enabled?: boolean;
};
function safeID(value: string): number {
  const n = Number(value);
  if (!/^[1-9][0-9]*$/.test(value) || !Number.isSafeInteger(n))
    throw new Error("Invalid event history identity.");
  return n;
}
export async function readAdminAutoscanEvents(
  profileContext: ProfileRequestContextSnapshot,
  params: AutoscanEventQuery,
): Promise<{ rows: AutoscanEvent[]; total: number }> {
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
    const result = await v2("GET /api/v2/admin/autoscan/events", {
      profileContext,
      query: { limit, cursor, status: params.status, q: params.query, source_id: params.sourceId },
    });
    if (!isCapturedProfileAuthorityActive(profileContext)) throw new StaleApiRequestContextError();
    if (
      !result.page ||
      typeof result.page.has_more !== "boolean" ||
      !Number.isSafeInteger(result.total) ||
      result.total < 0 ||
      result.items.length > limit
    )
      throw new Error("Invalid event history page.");
    const next = result.page.next_cursor;
    if (result.page.has_more && (!next || seen.has(next)))
      throw new Error("Invalid event history continuation.");
    if (page === offset / limit) {
      const ids = new Set<string>();
      const rows: AutoscanEvent[] = result.items.map((row) => {
        if (ids.has(row.id)) throw new Error("Duplicate event identity.");
        ids.add(row.id);
        if (!["running", "success", "error", "unresolved"].includes(row.status))
          throw new Error("Unsupported event state.");
        if (row.delivery_mode && !["poll", "webhook"].includes(row.delivery_mode))
          throw new Error("Unsupported event delivery.");
        const runIDs = new Set<string>();
        const scan_runs: AutoscanEventScanRun[] = row.scan_runs.map((run) => {
          if (!run.id || runIDs.has(run.id)) throw new Error("Invalid event scan identity.");
          runIDs.add(run.id);
          if (
            !["library", "subtree", "file"].includes(run.mode) ||
            !["accepted", "running", "completed", "failed", "cancelled"].includes(run.status)
          )
            throw new Error("Unsupported event scan state.");
          return {
            ...run,
            library_id: safeID(run.library_id),
            mode: run.mode as AutoscanEventScanRun["mode"],
            status: run.status as AutoscanEventScanRun["status"],
          };
        });
        return {
          ...row,
          id: safeID(row.id),
          source_id: row.source_id ?? null,
          status: row.status as AutoscanEventStatus,
          delivery_mode: row.delivery_mode as AutoscanEvent["delivery_mode"],
          scan_runs,
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
