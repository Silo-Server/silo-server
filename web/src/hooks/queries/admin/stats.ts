import { useQuery } from "@tanstack/react-query";
import { api } from "@/api/client";
import type { AdminStats, AdminSession, AdminLiveSessionsResponse } from "@/api/types";
import { adminKeys } from "../keys";

const ADMIN_STALE_TIME = 30_000;

export function fetchAdminStats(options: { refresh?: boolean } = {}) {
  return api<AdminStats>(`/admin/stats${options.refresh ? "?refresh=1" : ""}`);
}

export function useAdminStats() {
  return useQuery({
    queryKey: adminKeys.stats(),
    queryFn: () => fetchAdminStats(),
    staleTime: ADMIN_STALE_TIME,
  });
}

export function useAdminSessions() {
  return useQuery({
    queryKey: adminKeys.sessions(),
    queryFn: () => api<AdminSession[]>("/admin/sessions").then((d) => d ?? []),
    staleTime: ADMIN_STALE_TIME,
  });
}

/**
 * The live session list, from the merged stream-telemetry view.
 *
 * Where {@link useAdminSessions} answers "who told us they are watching?", this
 * answers "who is actually receiving video?". Every process publishes into that
 * one view — the five measuring route families, and each API process's session
 * manager as a reporting publisher — so the list already holds every session
 * anybody knows about, with each row carrying the evidence behind it:
 *
 * - `reported` — a client claims to be watching; nothing measured leaving.
 * - `measured` — bytes went out; no session manager claims them.
 * - `both` — an ordinary, corroborated viewer.
 *
 * Rows reported as playing with no measured delivery are held back unless
 * `includeHidden` is set; `no_delivery_count` says how many, whether or not they
 * are shown. Check `view_complete` before reading the count at face value: an
 * incomplete view is missing sessions by construction.
 *
 * @param includeHidden keep the rows reported as playing that delivered nothing.
 */
export function useAdminLiveSessions(includeHidden: boolean) {
  return useQuery({
    queryKey: adminKeys.liveSessions(includeHidden),
    queryFn: () =>
      api<AdminLiveSessionsResponse>(
        `/admin/sessions/live${includeHidden ? "?include_idle=true" : ""}`,
      ).then(
        (d) =>
          d ?? {
            telemetry_enabled: false,
            view_available: false,
            view_complete: false,
            view_stale: false,
            view_age_ms: 0,
            no_delivery_count: 0,
            no_delivery_shown: includeHidden,
            sessions: [],
          },
      ),
    staleTime: ADMIN_STALE_TIME,
  });
}
