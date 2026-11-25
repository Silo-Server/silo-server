import type { QueryClient } from "@tanstack/react-query";
import type { ProfileRequestContextSnapshot } from "@/api/client";
import type { AdminSession } from "@/api/types";
import { adminKeys } from "@/hooks/queries/keys";
import { adminAuthorityScope } from "./adminAuthority";

export function adminSessionsKey(context: ProfileRequestContextSnapshot | null) {
  return [...adminKeys.sessions(), adminAuthorityScope(context)] as const;
}

/**
 * Applies a `session.paused` or `session.resumed` event to the cached session
 * list, so a row shows the new state without waiting for the coalesced
 * re-read. The record with the higher lifecycle number decides, as
 * docs/realtime-api.md asks of monitors. Reports whether a row changed.
 */
export function applyAdminSessionPauseEvent(
  queryClient: QueryClient,
  context: ProfileRequestContextSnapshot | null,
  data: unknown,
): boolean {
  if (typeof data !== "object" || data === null) return false;
  const { session_id: id, seq, state } = data as Record<string, unknown>;
  if (typeof id !== "string" || typeof seq !== "number") return false;
  if (state !== "paused" && state !== "playing") return false;
  let changed = false;
  queryClient.setQueryData<AdminSession[]>(adminSessionsKey(context), (rows) =>
    rows?.map((row) => {
      if (row.session_id !== id || (row.lifecycle_seq ?? 0) >= seq) return row;
      changed = true;
      return { ...row, is_paused: state === "paused", lifecycle_seq: seq };
    }),
  );
  return changed;
}
