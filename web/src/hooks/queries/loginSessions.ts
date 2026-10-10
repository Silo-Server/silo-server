import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  captureSessionIdentity,
  isSessionIdentityCurrent,
  StaleApiRequestContextError,
} from "@/api/client";
import { v2, type V2Result } from "@/api/v2/request";
import { useAuth } from "@/hooks/useAuth";
import {
  captureAdminAuthority,
  requireAdminAuthority,
  adminAuthorityScope,
} from "@/api/v2/adminAuthority";

export type LoginSession = V2Result<"GET /api/v2/auth/sessions">["items"][number];

function requireSession(session: ReturnType<typeof captureSessionIdentity>) {
  if (!isSessionIdentityCurrent(session)) throw new StaleApiRequestContextError();
}

export function useLoginSessionCapabilities() {
  const session = captureSessionIdentity();
  const { profile } = useAuth();
  return useQuery({
    // Profile selection clears the shared query cache. Rebind the observer
    // after that transition, including the automatic selection after login.
    queryKey: [
      "login-session-capabilities",
      session.serverOrigin,
      session.authContextVersion,
      profile?.id ?? null,
    ],
    queryFn: () => v2("GET /api/v2/auth/sessions/capabilities"),
  });
}

export function useLoginSessions(adminUserId?: number, enabled = true) {
  const client = useQueryClient();
  const session = captureSessionIdentity();
  const { profile } = useAuth();
  const key = [
    "login-sessions",
    session.serverOrigin,
    session.authContextVersion,
    profile?.id ?? null,
    adminUserId ?? "self",
    adminUserId ? adminAuthorityScope() : "account",
  ];
  const query = useInfiniteQuery({
    queryKey: key,
    enabled,
    initialPageParam: undefined as string | undefined,
    staleTime: 15_000,
    refetchInterval: 60_000,
    queryFn: async ({ pageParam, signal }) => {
      requireSession(session);
      if (adminUserId) {
        const authority = captureAdminAuthority();
        const result = await v2("GET /api/v2/admin/users/{user_id}/login-sessions", {
          path: { user_id: String(adminUserId) },
          query: { cursor: pageParam, limit: 50 },
          signal,
          profileContext: authority,
        });
        requireAdminAuthority(authority);
        if (!result.page) throw new Error("Invalid session pagination. Reload the list.");
        return { ...result, page: result.page };
      }
      const result = await v2("GET /api/v2/auth/sessions", {
        query: { cursor: pageParam, limit: 50 },
        signal,
      });
      requireSession(session);
      if (!result.page) throw new Error("Invalid session pagination. Reload the list.");
      return { ...result, page: result.page };
    },
    getNextPageParam: (lastPage, pages) => {
      if (!lastPage.page.has_more) return undefined;
      const next = lastPage.page.next_cursor;
      if (!next || pages.slice(0, -1).some((page) => page.page.next_cursor === next))
        throw new Error("Couldn't load more sessions. Reload the list.");
      return next;
    },
  });
  const revoke = useMutation({
    retry: false,
    mutationFn: async (id: string) => {
      const submittedSession = captureSessionIdentity();
      if (adminUserId) {
        const authority = captureAdminAuthority();
        await v2("DELETE /api/v2/admin/users/{user_id}/login-sessions/{session_id}", {
          path: { user_id: String(adminUserId), session_id: id },
          profileContext: authority,
          retryAuthentication: false,
        });
        requireAdminAuthority(authority);
      } else {
        await v2("DELETE /api/v2/auth/sessions/{id}", { path: { id }, retryAuthentication: false });
        requireSession(submittedSession);
      }
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: key });
    },
  });
  const revokeAll = useMutation({
    retry: false,
    mutationFn: async () => {
      if (!adminUserId) throw new Error("Choose an account before signing out its sessions.");
      const authority = captureAdminAuthority();
      const result = await v2("DELETE /api/v2/admin/users/{user_id}/login-sessions", {
        path: { user_id: String(adminUserId) },
        profileContext: authority,
        retryAuthentication: false,
      });
      requireAdminAuthority(authority);
      return result;
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: key });
    },
  });
  const unique = new Map<string, LoginSession>();
  for (const page of query.data?.pages ?? []) for (const row of page.items) unique.set(row.id, row);
  return {
    query,
    sessions: [...unique.values()],
    currentSession: query.data?.pages[0]?.current_session ?? null,
    revoke,
    revokeAll,
  };
}
