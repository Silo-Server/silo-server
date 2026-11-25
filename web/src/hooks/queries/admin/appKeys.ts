import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { captureSessionIdentity } from "@/api/client";
import { v2, type V2Result } from "@/api/v2/request";
import {
  adminAuthorityScope,
  captureAdminAuthority,
  requireAdminAuthority,
} from "@/api/v2/adminAuthority";

export type AdminAppKeyList = V2Result<"GET /api/v2/admin/users/{user_id}/keys">;
export type AdminAppDeviceKey = AdminAppKeyList["devices"][number];

function keyScope(adminUserId: number) {
  const session = captureSessionIdentity();
  return [session.serverOrigin, session.authContextVersion, adminUserId, adminAuthorityScope()];
}

/**
 * One account's app keys and its device sign-ins approved in advance, for an
 * administrator, with the actions on them. Every action also refreshes the
 * account's login sessions: removing a device ends the sessions its key opened.
 */
export function useAdminAppKeys(adminUserId: number, enabled = true) {
  const client = useQueryClient();
  const scope = keyScope(adminUserId);
  const keysKey = ["admin-app-keys", ...scope];
  const preapprovalsKey = ["admin-device-preapprovals", ...scope];
  const keys = useQuery<AdminAppKeyList>({
    queryKey: keysKey,
    enabled,
    staleTime: 15_000,
    // A waiting device leaves the list when it is approved or its 24 hours end.
    refetchInterval: (query) =>
      query.state.data?.devices.some((key) => key.status === "pending") ? 30_000 : 60_000,
    queryFn: async ({ signal }) => {
      const authority = captureAdminAuthority();
      const result = await v2("GET /api/v2/admin/users/{user_id}/keys", {
        path: { user_id: String(adminUserId) },
        signal,
        profileContext: authority,
      });
      requireAdminAuthority(authority);
      return result;
    },
  });
  const preapprovals = useQuery({
    queryKey: preapprovalsKey,
    enabled,
    staleTime: 15_000,
    refetchInterval: 60_000,
    queryFn: async ({ signal }) => {
      const authority = captureAdminAuthority();
      const result = await v2("GET /api/v2/admin/users/{user_id}/device-preapprovals", {
        path: { user_id: String(adminUserId) },
        signal,
        profileContext: authority,
      });
      requireAdminAuthority(authority);
      return result.items;
    },
  });
  const path = { user_id: String(adminUserId) };
  const settle = () => {
    void client.invalidateQueries({ queryKey: keysKey });
    void client.invalidateQueries({ queryKey: preapprovalsKey });
    void client.invalidateQueries({ queryKey: ["login-sessions"] });
  };
  const run = async (
    call: (authority: ReturnType<typeof captureAdminAuthority>) => Promise<unknown>,
  ) => {
    const authority = captureAdminAuthority();
    await call(authority);
    requireAdminAuthority(authority);
  };
  const revokeDevice = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      run((authority) =>
        v2("DELETE /api/v2/admin/users/{user_id}/keys/devices/{id}", {
          path: { ...path, id },
          profileContext: authority,
          retryAuthentication: false,
        }),
      ),
    onSettled: settle,
  });
  const revokePersonKey = useMutation({
    retry: false,
    mutationFn: () =>
      run((authority) =>
        v2("DELETE /api/v2/admin/users/{user_id}/keys/person", {
          path,
          profileContext: authority,
          retryAuthentication: false,
        }),
      ),
    onSettled: settle,
  });
  const deny = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      run((authority) =>
        v2("POST /api/v2/admin/users/{user_id}/keys/pending/{id}/deny", {
          path: { ...path, id },
          body: {},
          profileContext: authority,
          retryAuthentication: false,
        }),
      ),
    onSettled: settle,
  });
  const withdraw = useMutation({
    retry: false,
    mutationFn: (id: string) =>
      run((authority) =>
        v2("DELETE /api/v2/admin/users/{user_id}/device-preapprovals/{id}", {
          path: { ...path, id },
          profileContext: authority,
          retryAuthentication: false,
        }),
      ),
    onSettled: settle,
  });
  return { keys, preapprovals, revokeDevice, revokePersonKey, deny, withdraw };
}
