import type { QueryClient } from "@tanstack/react-query";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminSession } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope } from "./adminAuthority";

export function adminSessionsKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.sessions(), adminAuthorityScope(context)] as const;
}

// A session's pause state is decided by the record with the higher lifecycle
// number, as docs/realtime-api.md asks of monitors; a row without one counts
// as number 0.
function newerPauseState(row: AdminSession, seq: number | null | undefined) {
  return (seq ?? 0) > (row.lifecycle_seq ?? 0);
}

/**
 * Applies a `session.paused` or `session.resumed` event to the cached session
 * list, so a row shows the new state without waiting for the coalesced
 * re-read.
 */
export function applyAdminSessionPauseEvent(
  queryClient: QueryClient,
  context: ProfileRequestContextSnapshot | null,
  data: unknown,
) {
  if (typeof data !== "object" || data === null) return;
  const { session_id: id, seq, state } = data as Record<string, unknown>;
  if (typeof id !== "string" || typeof seq !== "number") return;
  if (state !== "paused" && state !== "playing") return;
  queryClient.setQueryData<AdminSession[]>(adminSessionsKey(context), (rows) =>
    rows?.map((row) =>
      row.session_id === id && newerPauseState(row, seq)
        ? { ...row, is_paused: state === "paused", lifecycle_seq: seq }
        : row,
    ),
  );
}

/**
 * Reads the session list for the cache, keeping a pause state the cache took
 * from an event while the read was in flight: that event is newer than the
 * read, which would otherwise put the old state back.
 */
export async function readAdminSessionsKeepingPauseEvents(
  queryClient: QueryClient,
  context: ProfileRequestContextSnapshot | null,
  read: () => Promise<AdminSession[]>,
): Promise<AdminSession[]> {
  const fetched = await read();
  // Look at the cache only now, after the read: the events that matter are
  // the ones applied while it ran.
  const cached = queryClient.getQueryData<AdminSession[]>(adminSessionsKey(context));
  if (!cached?.length) return fetched;
  const byID = new Map(cached.map((row) => [row.session_id, row]));
  return fetched.map((row) => {
    const previous = byID.get(row.session_id);
    return previous && newerPauseState(row, previous.lifecycle_seq)
      ? { ...row, is_paused: previous.is_paused, lifecycle_seq: previous.lifecycle_seq }
      : row;
  });
}
