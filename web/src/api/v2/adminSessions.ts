import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminSession } from "@/api/types";
import { requireAdminUserAuthority } from "./adminUsers";
import { v2, type V2Result } from "./request";

type Session = V2Result<"GET /api/v2/admin/sessions">["items"][number];
function numericID(raw: string) {
  const id = Number(raw);
  if (!Number.isSafeInteger(id) || id < 0 || String(id) !== raw)
    throw new Error("Unsupported session identifier.");
  return id;
}
function sessionOf(row: Session): AdminSession {
  return {
    ...row,
    user_id: numericID(row.user_id),
    media_file_id: numericID(row.media_file_id),
    requested_media_file_id: numericID(row.requested_media_file_id),
    routing_execution_node_id:
      row.routing_execution_node_id == null ? undefined : numericID(row.routing_execution_node_id),
    routing_egress_node_id:
      row.routing_egress_node_id == null ? undefined : numericID(row.routing_egress_node_id),
  };
}
// The list is a live observation. Refresh may find sessions inserted behind a
// cursor; no page grants sequenced control or confirms transport revocation.
export async function listAdminPlaybackSessions(
  context: ProfileRequestContextSnapshot,
): Promise<AdminSession[]> {
  const rows = new Map<string, AdminSession>();
  const seen = new Set<string>();
  let cursor: string | undefined;
  for (let pageIndex = 0; pageIndex < 100; pageIndex++) {
    requireAdminUserAuthority(context);
    const page = await v2("GET /api/v2/admin/sessions", {
      profileContext: context,
      query: { limit: 100, cursor },
    });
    requireAdminUserAuthority(context);
    if (!page.page || typeof page.page.has_more !== "boolean")
      throw new Error("Invalid session page.");
    for (const row of page.items) rows.set(row.session_id, sessionOf(row));
    if (!page.page.has_more) return [...rows.values()];
    const next = page.page.next_cursor;
    if (!next || seen.has(next)) throw new Error("Invalid session continuation.");
    seen.add(next);
    cursor = next;
  }
  throw new Error("Too many sessions to display. Reload the list.");
}
