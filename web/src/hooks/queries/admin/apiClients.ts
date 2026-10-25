import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { captureAdminAuthority, requireAdminAuthority } from "@/api/v2/adminAuthority";
import { v2, type V2Result } from "@/api/v2/request";
import { adminKeys } from "../keys";

export type AdminApiClients = V2Result<"GET /api/v2/admin/stats/api-clients">;
export type AdminApiClient = AdminApiClients["clients"][number];
export type AdminApiClientEndpoints =
  V2Result<"GET /api/v2/admin/stats/api-clients/{session_id}/endpoints">;

/** Mirrors the server's window: 1 hour to 30 days. */
const MAX_HOURS = 720;
/** The summaries are rewritten every 15 minutes; a minute of staleness is invisible. */
const API_CLIENTS_STALE_TIME = 55_000;
const API_CLIENTS_LIMIT = 10;

export function normalizeApiClientHours(hours: number): number {
  return Number.isFinite(hours) ? Math.min(Math.max(Math.trunc(hours), 1), MAX_HOURS) : 24;
}

/** The sign-in sessions with the most API requests over the last `hours`. */
export function useAdminApiClients(hours: number) {
  const window = normalizeApiClientHours(hours);
  return useQuery({
    queryKey: adminKeys.apiClients(window),
    queryFn: async (): Promise<AdminApiClients> => {
      const authority = captureAdminAuthority();
      const result = await v2("GET /api/v2/admin/stats/api-clients", {
        profileContext: authority,
        query: { hours: window, limit: API_CLIENTS_LIMIT },
      });
      requireAdminAuthority(authority);
      return result;
    },
    staleTime: API_CLIENTS_STALE_TIME,
  });
}

/** The routes one session called most; fetched only when its row is open. */
export function useAdminApiClientEndpoints(sessionId: string | null, hours: number) {
  const window = normalizeApiClientHours(hours);
  return useQuery({
    queryKey: adminKeys.apiClientEndpoints(sessionId ?? "", window),
    enabled: sessionId !== null,
    queryFn: async (): Promise<AdminApiClientEndpoints> => {
      const authority = captureAdminAuthority();
      const result = await v2("GET /api/v2/admin/stats/api-clients/{session_id}/endpoints", {
        profileContext: authority,
        path: { session_id: sessionId ?? "" },
        query: { hours: window },
      });
      requireAdminAuthority(authority);
      return result;
    },
    staleTime: API_CLIENTS_STALE_TIME,
  });
}

/** Signs one session out. The account's other devices stay signed in. */
export function useRevokeApiClientSession() {
  const client = useQueryClient();
  return useMutation({
    retry: false,
    mutationFn: async ({ userId, sessionId }: { userId: string; sessionId: string }) => {
      const authority = captureAdminAuthority();
      await v2("DELETE /api/v2/admin/users/{user_id}/login-sessions/{session_id}", {
        path: { user_id: userId, session_id: sessionId },
        profileContext: authority,
        retryAuthentication: false,
      });
      requireAdminAuthority(authority);
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: adminKeys.apiClientsRoot() });
      void client.invalidateQueries({ queryKey: ["login-sessions"] });
    },
  });
}
